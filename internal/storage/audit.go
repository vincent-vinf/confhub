package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
	"gorm.io/gorm/clause"
)

// Identity and object names are snapshots, without foreign keys to mutable
// users/configurations. Deleting an account or configuration retains its audit.
type Audit struct {
	ID                int64           `json:"id,string"`
	RequestID         string          `json:"request_id"`
	CreatedAt         time.Time       `json:"created_at"`
	UserID            string          `json:"user_id"`
	Username          string          `json:"username"`
	AttemptedUsername string          `json:"attempted_username,omitempty"`
	IP                string          `json:"ip"`
	Action            string          `json:"action"`
	Subject           string          `json:"subject"`
	Method            string          `json:"method"`
	Result            string          `json:"result"`
	Status            int             `json:"status"`
	DurationMS        int64           `json:"duration_ms"`
	Details           json.RawMessage `json:"details"`
}
type AuditFilter struct {
	Before   int64
	Limit    int
	Username string
	IP       string
	Action   string
	Result   string
	From     time.Time
	To       time.Time
}
type AuditPage struct {
	Audits     []Audit `json:"audits"`
	NextBefore string  `json:"next_before,omitempty"`
}

func (s *Store) BeginAudit(ctx context.Context, a Audit) (int64, error) {
	if err := s.writable(); err != nil {
		return 0, err
	}
	row := auditRow{RequestID: a.RequestID, CreatedAt: a.CreatedAt.UnixMicro(), IP: a.IP, Action: a.Action, Subject: a.Subject, Method: a.Method, Result: "pending", DetailsJSON: "{}"}
	err := s.db.WithContext(ctx).Create(&row).Error
	return row.ID, err
}

// Persist the verified actor before entering a handler that can mutate state.
func (s *Store) IdentifyAudit(ctx context.Context, id int64, user User) error {
	if err := s.writable(); err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Model(&auditRow{}).Where(map[string]any{"id": id, "result": "pending"}).Updates(map[string]any{"user_id": user.ID, "username": user.Username})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return config.ErrConflict
	}
	return nil
}
func (s *Store) FinishAudit(ctx context.Context, a Audit) error {
	if err := s.writable(); err != nil {
		return err
	}
	if len(a.Details) == 0 {
		a.Details = json.RawMessage(`{}`)
	}
	result := s.db.WithContext(ctx).Model(&auditRow{}).Where(map[string]any{"id": a.ID, "result": "pending"}).Updates(map[string]any{
		"user_id": a.UserID, "username": a.Username, "attempted_username": a.AttemptedUsername, "action": a.Action, "subject": a.Subject, "result": a.Result, "status": a.Status, "duration_ms": a.DurationMS, "details_json": string(a.Details),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return config.ErrConflict
	}
	return nil
}
func (s *Store) Audits(ctx context.Context, f AuditFilter) (AuditPage, error) {
	result := AuditPage{Audits: []Audit{}}
	if f.Before < 0 || f.Limit < 1 || f.Limit > 100 || !f.From.IsZero() && !f.To.IsZero() && f.From.After(f.To) {
		return result, fmt.Errorf("%w: invalid audit range or page", config.ErrInvalid)
	}
	if len(f.Username) > 64 || len(f.Action) > 64 {
		return result, fmt.Errorf("%w: invalid audit filter", config.ErrInvalid)
	}
	if f.Result != "" && f.Result != "pending" && f.Result != "success" && f.Result != "failure" {
		return result, fmt.Errorf("%w: invalid audit result", config.ErrInvalid)
	}
	q := s.db.WithContext(ctx).Model(&auditRow{})
	if f.Before > 0 {
		q = q.Where(clause.Lt{Column: "id", Value: f.Before})
	}
	if f.Username != "" {
		q = q.Where(clause.Or(clause.Eq{Column: "username", Value: f.Username}, clause.Eq{Column: "attempted_username", Value: f.Username}))
	}
	if f.IP != "" {
		ip, err := netip.ParseAddr(f.IP)
		if err != nil || ip.Zone() != "" {
			return result, fmt.Errorf("%w: invalid IP", config.ErrInvalid)
		}
		q = q.Where(map[string]any{"ip": ip.Unmap().String()})
	}
	if f.Action != "" {
		q = q.Where(map[string]any{"action": f.Action})
	}
	if f.Result != "" {
		q = q.Where(map[string]any{"result": f.Result})
	}
	if !f.From.IsZero() {
		q = q.Where(clause.Gte{Column: "created_at", Value: f.From.UnixMicro()})
	}
	if !f.To.IsZero() {
		q = q.Where(clause.Lt{Column: "created_at", Value: f.To.UnixMicro()})
	}
	var rows []auditRow
	if err := q.Order("id DESC").Limit(f.Limit + 1).Find(&rows).Error; err != nil {
		return result, err
	}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		result.NextBefore = fmt.Sprint(rows[len(rows)-1].ID)
	}
	for _, row := range rows {
		result.Audits = append(result.Audits, Audit{ID: row.ID, RequestID: row.RequestID, CreatedAt: time.UnixMicro(row.CreatedAt).UTC(), UserID: row.UserID, Username: row.Username, AttemptedUsername: row.AttemptedUsername, IP: row.IP, Action: row.Action, Subject: row.Subject, Method: row.Method, Result: row.Result, Status: row.Status, DurationMS: row.DurationMS, Details: json.RawMessage(row.DetailsJSON)})
	}
	return result, nil
}
