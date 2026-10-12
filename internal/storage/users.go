package storage

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
	"github.com/vincent-vinf/confhub/internal/config"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrForbidden = errors.New("permission denied")
var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

type Grant struct {
	Namespace string `json:"namespace"`
	Group     string `json:"group"`
	Access    string `json:"access"`
}

type User struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	Enabled     bool    `json:"enabled"`
	SystemAdmin bool    `json:"system_admin"`
	ClientsRead bool    `json:"clients_read"`
	Revision    int64   `json:"revision"`
	Grants      []Grant `json:"grants"`
	AuthVersion int64   `json:"-"`
}

type UserInput struct {
	Enabled     bool    `json:"enabled"`
	SystemAdmin bool    `json:"system_admin"`
	ClientsRead bool    `json:"clients_read"`
	Grants      []Grant `json:"grants"`
}

// Empty namespace/group represent all namespaces/groups. Literal names never
// acquire wildcard semantics. Grants combine by union; write includes read.
func (u User) Allows(action, namespace, group string) bool {
	if !u.Enabled {
		return false
	}
	if u.SystemAdmin {
		return true
	}
	if action == "clients.read" {
		return u.ClientsRead
	}
	if action != "config.read" && action != "config.write" {
		return false
	}
	for _, grant := range u.Grants {
		if grant.Namespace != "" && grant.Namespace != namespace {
			continue
		}
		if group != "" && grant.Group != "" && grant.Group != group {
			continue
		}
		if grant.Access == "write" || (action == "config.read" && grant.Access == "read") {
			return true
		}
	}
	return false
}

func validateUserInput(input UserInput) error {
	if len(input.Grants) > 200 {
		return fmt.Errorf("%w: at most 200 grants", config.ErrInvalid)
	}
	seen := map[[2]string]bool{}
	for _, g := range input.Grants {
		if g.Access != "read" && g.Access != "write" {
			return fmt.Errorf("%w: access must be read or write", config.ErrInvalid)
		}
		if g.Namespace == "" && g.Group != "" {
			return fmt.Errorf("%w: group requires namespace", config.ErrInvalid)
		}
		for _, name := range []string{g.Namespace, g.Group} {
			if name != "" {
				if err := config.ValidateName(name); err != nil {
					return err
				}
			}
		}
		key := [2]string{g.Namespace, g.Group}
		if seen[key] {
			return fmt.Errorf("%w: duplicate grant", config.ErrInvalid)
		}
		seen[key] = true
	}
	return nil
}

func readUser(tx *gorm.DB, id string) (User, error) {
	var row userRow
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Take(&row, "id = ?", id).Error; err != nil {
		return User{}, storageError(err)
	}
	var grants []grantRow
	if err := tx.Where("user_id = ?", id).Order("namespace, group_name").Find(&grants).Error; err != nil {
		return User{}, err
	}
	u := User{ID: row.ID, Username: row.Username, Enabled: row.Enabled, SystemAdmin: row.SystemAdmin, ClientsRead: row.ClientsRead, Revision: row.Revision, AuthVersion: row.AuthVersion, Grants: []Grant{}}
	for _, g := range grants {
		u.Grants = append(u.Grants, Grant{g.Namespace, g.GroupName, g.Access})
	}
	return u, nil
}
func (s *Store) User(ctx context.Context, id string) (User, error) {
	var u User
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { var err error; u, err = readUser(tx, id); return err })
	return u, err
}
func (s *Store) Users(ctx context.Context, after string, limit int) ([]User, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("%w: limit must be 1–100", config.ErrInvalid)
	}
	users := []User{}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []userRow
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where(clause.Gt{Column: "username", Value: after}).Order("username").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			u, err := readUser(tx, row.ID)
			if err != nil {
				return err
			}
			users = append(users, u)
		}
		return nil
	})
	return users, err
}
func replaceGrants(tx *gorm.DB, id string, grants []Grant) error {
	if err := tx.Where("user_id = ?", id).Delete(&grantRow{}).Error; err != nil {
		return err
	}
	if len(grants) == 0 {
		return nil
	}
	rows := make([]grantRow, 0, len(grants))
	for _, g := range grants {
		rows = append(rows, grantRow{id, g.Namespace, g.Group, g.Access})
	}
	return tx.Create(&rows).Error
}
func (s *Store) CreateUser(ctx context.Context, username, password string, input UserInput) (User, error) {
	if !usernamePattern.MatchString(username) {
		return User{}, fmt.Errorf("%w: username must contain 1–64 ASCII letters, numbers, _, . or -", config.ErrInvalid)
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	if err := validateUserInput(input); err != nil {
		return User{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	row := userRow{ID: uuid.NewString(), Username: username, PasswordHash: string(hash), Enabled: input.Enabled, SystemAdmin: input.SystemAdmin, ClientsRead: input.ClientsRead, AuthVersion: 1, Revision: 1}
	tx, _, err := s.beginMutation(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if err = storageError(tx.Create(&row).Error); err != nil {
		return User{}, err
	}
	if err = replaceGrants(tx, row.ID, input.Grants); err != nil {
		return User{}, err
	}
	u, err := readUser(tx, row.ID)
	if err != nil {
		return User{}, err
	}
	return u, tx.Commit().Error
}
func (s *Store) UpdateUser(ctx context.Context, id string, revision int64, input UserInput) (User, error) {
	if err := validateUserInput(input); err != nil {
		return User{}, err
	}
	tx, _, err := s.beginMutation(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	u, err := readUser(tx, id)
	if err != nil {
		return User{}, err
	}
	if u.Revision != revision {
		return User{}, config.ErrConflict
	}
	if u.Username == "admin" && (!input.Enabled || !input.SystemAdmin) {
		return User{}, ErrForbidden
	}
	values := map[string]any{"enabled": input.Enabled, "system_admin": input.SystemAdmin, "clients_read": input.ClientsRead, "revision": u.Revision + 1}
	if u.Enabled != input.Enabled {
		values["auth_version"] = u.AuthVersion + 1
	}
	if err = tx.Model(&userRow{}).Where("id = ?", id).Updates(values).Error; err != nil {
		return User{}, err
	}
	if err = replaceGrants(tx, id, input.Grants); err != nil {
		return User{}, err
	}
	u, err = readUser(tx, id)
	if err != nil {
		return User{}, err
	}
	return u, tx.Commit().Error
}
func (s *Store) DeleteUser(ctx context.Context, id string, revision int64) error {
	tx, _, err := s.beginMutation(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	u, err := readUser(tx, id)
	if err != nil {
		return err
	}
	if u.Username == "admin" {
		return ErrForbidden
	}
	if u.Revision != revision {
		return config.ErrConflict
	}
	if err = tx.Where("id = ?", id).Delete(&userRow{}).Error; err != nil {
		return err
	}
	return tx.Commit().Error
}
func (s *Store) ResetPassword(ctx context.Context, id string, revision int64, password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Model(&userRow{}).Where("id = ? AND revision = ?", id, revision).Updates(map[string]any{"password_hash": string(hash), "auth_version": gorm.Expr("auth_version + 1"), "revision": gorm.Expr("revision + 1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return config.ErrConflict
	}
	return nil
}

func (s *Store) Authenticate(ctx context.Context, username, password string) (User, error) {
	var row userRow
	err := s.db.WithContext(ctx).Take(&row, "username = ?", username).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return User{}, err
	}
	// Use a valid dummy hash so unknown accounts also perform bcrypt work.
	hash := row.PasswordHash
	if hash == "" {
		hash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
	}
	if len(password) > 72 {
		return User{}, ErrUnauthorized
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || err != nil || !row.Enabled {
		return User{}, ErrUnauthorized
	}
	u, err := s.User(ctx, row.ID)
	if errors.Is(err, config.ErrNotFound) {
		return User{}, ErrUnauthorized
	}
	if err == nil && (!u.Enabled || u.AuthVersion != row.AuthVersion) {
		return User{}, ErrUnauthorized
	}
	return u, err
}
