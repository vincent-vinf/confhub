package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vincent-vinf/confhub/internal/clientinfo"
	"github.com/vincent-vinf/confhub/internal/config"
	"gorm.io/gorm/clause"
)

// SyncPresence atomically refreshes a replica lease and reconciles its complete
// snapshot. Stable connections are not rewritten; expired replicas are invisible.
// This transaction is independent of the configuration mutation/change stream.
func (s *Store) SyncPresence(ctx context.Context, instance string, clients []clientinfo.Client, ttl time.Duration) error {
	if instance == "" || len(instance) > 36 || ttl <= 0 {
		return fmt.Errorf("%w: invalid presence lease", config.ErrInvalid)
	}
	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback()
	now, err := s.databaseTime(tx)
	if err != nil {
		return err
	}
	if err = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"refreshed_at", "expires_at"}),
	}).Create(&instanceRow{ID: instance, RefreshedAt: now, ExpiresAt: now + ttl.Microseconds()}).Error; err != nil {
		return err
	}
	var previousRows []clientRow
	if err = tx.Select("id", "fingerprint").Where(map[string]any{"instance_id": instance}).Find(&previousRows).Error; err != nil {
		return err
	}
	previous := map[string]string{}
	for _, row := range previousRows {
		previous[row.ID] = row.Fingerprint
	}
	seen := map[string]bool{}
	clientRows, tagRows := []clientRow{}, []tagRow{}
	removals := []string{}
	batchBytes := 0
	// Bounded bulk writes keep connection churn from turning each tag into a
	// separate round trip. Stable connections still require no row updates.
	deleteClients := func(ids []string) error {
		if len(ids) == 0 {
			return nil
		}
		return tx.Where(map[string]any{"instance_id": instance}).
			Where(clause.IN{Column: "id", Values: stringValues(ids)}).Delete(&clientRow{}).Error
	}
	flush := func() error {
		if e := deleteClients(removals); e != nil {
			return e
		}
		if len(clientRows) > 0 {
			if e := tx.Create(&clientRows).Error; e != nil {
				return e
			}
		}
		if len(tagRows) > 0 {
			if e := tx.Create(&tagRows).Error; e != nil {
				return e
			}
		}
		removals = nil
		clientRows = nil
		tagRows = nil
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
		clientRows = append(clientRows, clientRow{ID: c.ID, InstanceID: instance, Fingerprint: fingerprint, SnapshotJSON: string(raw)})
		batchBytes += len(raw)
		for name, value := range c.Tags {
			tagRows = append(tagRows, tagRow{ClientID: c.ID, Name: []byte(name), Value: []byte(value)})
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
	return tx.Commit().Error
}

func stringValues(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}

func (s *Store) RemovePresence(ctx context.Context, instance string) error {
	return s.db.WithContext(ctx).Where(map[string]any{"id": instance}).Delete(&instanceRow{}).Error
}

func (s *Store) Clients(ctx context.Context, after string, limit int) (clientinfo.Page, error) {
	page := clientinfo.Page{Clients: []clientinfo.Client{}}
	if limit < 1 || limit > 100 || len(after) > 36 {
		return page, fmt.Errorf("%w: invalid client page", config.ErrInvalid)
	}
	db := s.db.WithContext(ctx)
	now, err := s.databaseTime(db)
	if err != nil {
		return page, err
	}
	var rows []struct {
		SnapshotJSON string `gorm:"column:snapshot_json"`
		RefreshedAt  int64
	}
	err = db.Table("connected_clients AS c").Select("c.snapshot_json", "i.refreshed_at").Clauses(clause.From{
		Tables: []clause.Table{{Name: "connected_clients", Alias: "c"}},
		Joins: []clause.Join{{Type: clause.InnerJoin, Table: clause.Table{Name: "client_instances", Alias: "i"},
			ON: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: column("i", "id"), Value: column("c", "instance_id")}}}}},
	}).Where(clause.Gt{Column: column("i", "expires_at"), Value: now}).
		Where(clause.Gt{Column: column("c", "id"), Value: after}).
		Order(clause.OrderByColumn{Column: column("c", "id")}).Limit(limit + 1).Scan(&rows).Error
	if err != nil {
		return page, err
	}
	for _, row := range rows {
		var c clientinfo.Client
		if err = json.Unmarshal([]byte(row.SnapshotJSON), &c); err != nil {
			return page, err
		}
		c.RefreshedAt = time.UnixMicro(row.RefreshedAt).UTC()
		page.Clients = append(page.Clients, c)
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
	db := s.db.WithContext(ctx)
	now, err := s.databaseTime(db)
	if err != nil {
		return nil, err
	}
	field := column("t", "name")
	if tag != "" {
		field.Name = "value"
	}
	query := db.Table("client_tags AS t").Clauses(clause.From{
		Tables: []clause.Table{{Name: "client_tags", Alias: "t"}},
		Joins: []clause.Join{
			{Type: clause.InnerJoin, Table: clause.Table{Name: "connected_clients", Alias: "c"},
				ON: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: column("c", "id"), Value: column("t", "client_id")}}}},
			{Type: clause.InnerJoin, Table: clause.Table{Name: "client_instances", Alias: "i"},
				ON: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: column("i", "id"), Value: column("c", "instance_id")}}}},
		},
	}).Where(clause.Gt{Column: column("i", "expires_at"), Value: now}).Where(s.tagPrefix(field, prefix))
	if tag != "" {
		query = query.Where(clause.Eq{Column: column("t", "name"), Value: []byte(tag)})
	}
	var rows []struct{ Value []byte }
	selected := field
	selected.Alias = "value"
	err = query.Clauses(clause.Select{Distinct: true, Columns: []clause.Column{selected}}).
		Order(clause.OrderByColumn{Column: field}).Limit(100).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	suggestions := map[string]bool{}
	for _, row := range rows {
		suggestions[string(row.Value)] = true
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
	db := s.db.WithContext(ctx)
	now, err := s.databaseTime(db)
	if err != nil {
		return err
	}
	var ids []string
	if err = db.Model(&instanceRow{}).Where(clause.Lte{Column: "expires_at", Value: now}).
		Order("expires_at").Limit(32).Pluck("id", &ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		if err = db.Where(map[string]any{"id": id}).Where(clause.Lte{Column: "expires_at", Value: now}).Delete(&instanceRow{}).Error; err != nil {
			return err
		}
	}
	return nil
}
