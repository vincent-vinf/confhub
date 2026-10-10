package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitlab.bodesitech.com/bodesi/confhub/internal/clientinfo"
	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
)

func (s *Store) clockQuery() string {
	if s.dialect == "mysql" {
		return "SELECT CAST(UNIX_TIMESTAMP(NOW(6))*1000000 AS SIGNED)"
	}
	return "SELECT CAST(EXTRACT(EPOCH FROM clock_timestamp())*1000000 AS BIGINT)"
}

// SyncPresence atomically refreshes a replica lease and reconciles its complete
// snapshot. Stable connections are not rewritten; expired replicas are invisible.
// This transaction is independent of the configuration mutation/change stream.
func (s *Store) SyncPresence(ctx context.Context, instance string, clients []clientinfo.Client, ttl time.Duration) error {
	if instance == "" || len(instance) > 36 || ttl <= 0 {
		return fmt.Errorf("%w: invalid presence lease", config.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var now int64
	if err = tx.QueryRowContext(ctx, s.clockQuery()).Scan(&now); err != nil {
		return err
	}
	q := "INSERT INTO client_instances(id,refreshed_at,expires_at) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET refreshed_at=EXCLUDED.refreshed_at,expires_at=EXCLUDED.expires_at"
	if s.dialect == "mysql" {
		q = "INSERT INTO client_instances(id,refreshed_at,expires_at) VALUES(?,?,?) ON DUPLICATE KEY UPDATE refreshed_at=VALUES(refreshed_at),expires_at=VALUES(expires_at)"
	}
	if _, err = tx.ExecContext(ctx, s.query(q), instance, now, now+ttl.Microseconds()); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, s.query("SELECT id,fingerprint FROM connected_clients WHERE instance_id=?"), instance)
	if err != nil {
		return err
	}
	previous := map[string]string{}
	for rows.Next() {
		var id, fingerprint string
		if err = rows.Scan(&id, &fingerprint); err != nil {
			break
		}
		previous[id] = fingerprint
	}
	if err = errorsJoinRows(err, rows); err != nil {
		return err
	}
	seen := map[string]bool{}
	clientArgs, tagArgs := []any{}, []any{}
	removals := []string{}
	batchBytes := 0
	// Bounded bulk writes keep connection churn from turning each tag into a
	// separate round trip. Stable connections still require no row updates.
	deleteClients := func(ids []string) error {
		if len(ids) == 0 {
			return nil
		}
		args := []any{instance}
		for _, id := range ids {
			args = append(args, id)
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		_, e := tx.ExecContext(ctx, s.query("DELETE FROM connected_clients WHERE instance_id=? AND id IN ("+placeholders+")"), args...)
		return e
	}
	insertRows := func(prefix string, columns int, args []any) error {
		if len(args) == 0 {
			return nil
		}
		row := "(" + strings.TrimSuffix(strings.Repeat("?,", columns), ",") + ")"
		_, e := tx.ExecContext(ctx, s.query(prefix+strings.TrimSuffix(strings.Repeat(row+",", len(args)/columns), ",")), args...)
		return e
	}
	flush := func() error {
		if e := deleteClients(removals); e != nil {
			return e
		}
		if e := insertRows("INSERT INTO connected_clients(id,instance_id,fingerprint,snapshot_json) VALUES ", 4, clientArgs); e != nil {
			return e
		}
		if e := insertRows("INSERT INTO client_tags(client_id,name,value) VALUES ", 3, tagArgs); e != nil {
			return e
		}
		removals = nil
		clientArgs = nil
		tagArgs = nil
		batchBytes = 0
		return nil
	}
	for _, c := range clients {
		if c.ID == "" || len(c.ID) > 36 || seen[c.ID] {
			return fmt.Errorf("%w: invalid client identity", config.ErrInvalid)
		}
		if err = config.ValidateTags(c.Tags); err != nil {
			return err
		}
		seen[c.ID] = true
		c.InstanceID = instance
		c.RefreshedAt = time.Time{}
		raw, e := json.Marshal(c)
		if e != nil {
			return e
		}
		fingerprint := fmt.Sprintf("%x", sha256.Sum256(raw))
		if previous[c.ID] == fingerprint {
			continue
		}
		removals = append(removals, c.ID)
		clientArgs = append(clientArgs, c.ID, instance, fingerprint, string(raw))
		batchBytes += len(raw)
		for name, value := range c.Tags {
			tagArgs = append(tagArgs, c.ID, []byte(name), []byte(value))
		}
		if len(removals) >= 32 || batchBytes >= 256<<10 {
			if err = flush(); err != nil {
				return err
			}
		}
	}
	if err = flush(); err != nil {
		return err
	}
	for id := range previous {
		if !seen[id] {
			removals = append(removals, id)
			if len(removals) >= 32 {
				if err = deleteClients(removals); err != nil {
					return err
				}
				removals = nil
			}
		}
	}
	if err = deleteClients(removals); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RemovePresence(ctx context.Context, instance string) error {
	_, err := s.db.ExecContext(ctx, s.query("DELETE FROM client_instances WHERE id=?"), instance)
	return err
}

func (s *Store) Clients(ctx context.Context, after string, limit int) (clientinfo.Page, error) {
	page := clientinfo.Page{Clients: []clientinfo.Client{}}
	if limit < 1 || limit > 100 || len(after) > 36 {
		return page, fmt.Errorf("%w: invalid client page", config.ErrInvalid)
	}
	var now int64
	if err := s.db.QueryRowContext(ctx, s.clockQuery()).Scan(&now); err != nil {
		return page, err
	}
	rows, err := s.db.QueryContext(ctx, s.query("SELECT c.snapshot_json,i.refreshed_at FROM connected_clients c JOIN client_instances i ON i.id=c.instance_id WHERE i.expires_at>? AND c.id>? ORDER BY c.id LIMIT ?"), now, after, limit+1)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var raw string
		var refreshed int64
		var c clientinfo.Client
		if err = rows.Scan(&raw, &refreshed); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &c); err != nil {
			break
		}
		c.RefreshedAt = time.UnixMicro(refreshed).UTC()
		page.Clients = append(page.Clients, c)
	}
	if err = errorsJoinRows(err, rows); err != nil {
		return page, err
	}
	if len(page.Clients) > limit {
		page.Clients = page.Clients[:limit]
		page.NextAfter = page.Clients[limit-1].ID
	}
	return page, nil
}

// TagSuggestions queries all live replicas. Prefixes are escaped literals, not
// SQL wildcards. Results are distinct, sorted, and bounded independently of pages.
func (s *Store) TagSuggestions(ctx context.Context, tag, prefix string) ([]string, error) {
	if len(tag) > 128 || len(prefix) > 512 {
		return nil, fmt.Errorf("%w: invalid suggestion query", config.ErrInvalid)
	}
	var now int64
	if err := s.db.QueryRowContext(ctx, s.clockQuery()).Scan(&now); err != nil {
		return nil, err
	}
	column := "t.name"
	if tag != "" {
		column = "t.value"
	}
	pattern := "? ESCAPE '!'"
	if s.dialect == "postgres" {
		pattern = "?::bytea ESCAPE '!'::bytea"
	}
	q := "SELECT DISTINCT " + column + " FROM client_tags t JOIN connected_clients c ON c.id=t.client_id JOIN client_instances i ON i.id=c.instance_id WHERE i.expires_at>? AND " + column + " LIKE " + pattern
	escape := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(prefix) + "%"
	args := []any{now, []byte(escape)}
	if tag != "" {
		q += " AND t.name=?"
		args = append(args, []byte(tag))
	}
	q += " ORDER BY " + column + " LIMIT 100"
	rows, err := s.db.QueryContext(ctx, s.query(q), args...)
	if err != nil {
		return nil, err
	}
	suggestions := map[string]bool{}
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			break
		}
		suggestions[value] = true
	}
	if err = errorsJoinRows(err, rows); err != nil {
		return nil, err
	}
	if tag == "" {
		for _, name := range []string{"sys.ip", "sys.hostname"} {
			if strings.HasPrefix(name, prefix) {
				suggestions[name] = true
			}
		}
	}
	values := []string{}
	for value := range suggestions {
		values = append(values, value)
	}
	sort.Strings(values)
	if len(values) > 100 {
		// Reserve slots for built-ins even when many custom names sort first.
		reserved := []string{}
		if tag == "" {
			for _, name := range []string{"sys.hostname", "sys.ip"} {
				if strings.HasPrefix(name, prefix) {
					reserved = append(reserved, name)
				}
			}
		}
		bounded := []string{}
		for _, value := range values {
			if tag == "" && (value == "sys.ip" || value == "sys.hostname") {
				continue
			}
			if len(bounded) < 100-len(reserved) {
				bounded = append(bounded, value)
			}
		}
		values = append(bounded, reserved...)
		sort.Strings(values)
	}
	return values, nil
}

// CleanupPresence removes a bounded batch of abandoned replicas. A refreshed
// lease is rechecked at deletion so a concurrent heartbeat is not removed.
func (s *Store) CleanupPresence(ctx context.Context) error {
	var now int64
	if err := s.db.QueryRowContext(ctx, s.clockQuery()).Scan(&now); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, s.query("SELECT id FROM client_instances WHERE expires_at<=? ORDER BY expires_at LIMIT 32"), now)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err = errorsJoinRows(err, rows); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = s.db.ExecContext(ctx, s.query("DELETE FROM client_instances WHERE id=? AND expires_at<=?"), id, now); err != nil {
			return err
		}
	}
	return nil
}
