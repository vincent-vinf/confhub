package storage

import (
	"time"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
	"gorm.io/gorm"
)

// These mappings describe the migrate-managed schema; GORM never creates or
// alters tables. Keep persistence rows separate from the public domain model.
type namespaceRow struct {
	Name string `gorm:"primaryKey"`
}

func (namespaceRow) TableName() string { return "namespaces" }

type groupRow struct {
	Namespace string `gorm:"primaryKey"`
	Name      string `gorm:"primaryKey"`
}

func (groupRow) TableName() string { return "config_groups" }

type configRow struct {
	ID            string `gorm:"primaryKey"`
	Namespace     string
	GroupName     string
	Name          string
	Revision      int64
	LastVersion   int64
	GlobalVersion int64
}

func (configRow) TableName() string { return "configs" }

type versionRow struct {
	ConfigID      string `gorm:"primaryKey"`
	Number        int64  `gorm:"primaryKey;autoIncrement:false"`
	Content       string
	Format        string
	Description   string
	Action        string
	SourceVersion int64
	CreatedAt     int64 `gorm:"autoCreateTime:false"`
}

func (versionRow) TableName() string { return "config_versions" }

func (v versionRow) version() config.Version {
	return config.Version{Number: v.Number, Content: v.Content, Format: v.Format,
		Description: v.Description, Action: v.Action, SourceVersion: v.SourceVersion,
		CreatedAt: time.UnixMicro(v.CreatedAt).UTC()}
}

type betaRow struct {
	ConfigID string `gorm:"primaryKey"`
	BetaJSON string `gorm:"column:beta_json"`
}

func (betaRow) TableName() string { return "config_beta" }

type ruleRow struct {
	ConfigID string `gorm:"primaryKey"`
	ID       string `gorm:"primaryKey"`
	Position int
	RuleJSON string `gorm:"column:rule_json"`
}

func (ruleRow) TableName() string { return "gray_rules" }

type streamRow struct {
	ID            int `gorm:"primaryKey;autoIncrement:false"`
	Sequence      int64
	PurgedThrough int64
}

func (streamRow) TableName() string { return "change_stream" }

type eventRow struct {
	Sequence  int64 `gorm:"primaryKey;autoIncrement:false"`
	ConfigID  string
	Namespace string
	GroupName string
	Name      string
	Deleted   bool
	CreatedAt int64 `gorm:"autoCreateTime:false"`
}

func (eventRow) TableName() string { return "change_events" }

type leaseRow struct {
	ID        int `gorm:"primaryKey;autoIncrement:false"`
	Owner     string
	ExpiresAt int64
}

func (leaseRow) TableName() string { return "maintenance_lease" }

type adminRow struct {
	ID           int `gorm:"primaryKey;autoIncrement:false"`
	PasswordHash string
}

func (adminRow) TableName() string { return "admin" }

type instanceRow struct {
	ID          string `gorm:"primaryKey"`
	RefreshedAt int64
	ExpiresAt   int64
}

func (instanceRow) TableName() string { return "client_instances" }

type clientRow struct {
	ID           string `gorm:"primaryKey"`
	InstanceID   string
	Fingerprint  string
	SnapshotJSON string `gorm:"column:snapshot_json"`
}

func (clientRow) TableName() string { return "connected_clients" }

type tagRow struct {
	ClientID string `gorm:"primaryKey"`
	Name     []byte `gorm:"primaryKey"`
	Value    []byte
}

func (tagRow) TableName() string { return "client_tags" }

func configKey(db *gorm.DB, k config.Key) *gorm.DB {
	return db.Where(map[string]any{"namespace": k.Namespace, "group_name": k.Group, "name": k.Name})
}

func loadVersion(db *gorm.DB, id string, number int64) (config.Version, error) {
	var row versionRow
	err := db.Where(map[string]any{"config_id": id, "number": number}).Take(&row).Error
	return row.version(), storageError(err)
}
