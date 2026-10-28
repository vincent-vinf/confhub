package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/vincent-vinf/confhub/internal/config"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var ErrUnauthorized = errors.New("invalid credentials")

func validatePassword(password string) error {
	if len(password) < 8 || len(password) > 72 {
		return fmt.Errorf("%w: password must contain 8–72 bytes", config.ErrInvalid)
	}
	return nil
}
func (s *Store) InitializeAdmin(ctx context.Context, password string) error {
	if err := s.writable(); err != nil {
		return err
	}
	var row userRow
	err := s.db.WithContext(ctx).Take(&row, "username = ?", "admin").Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err = validatePassword(password); err != nil {
		return fmt.Errorf("first initialization requires admin-password: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, _, err := s.beginMutation(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = tx.Take(&row, "username = ?", "admin").Error; err == nil {
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	row = userRow{ID: uuid.NewString(), Username: "admin", PasswordHash: string(hash), Enabled: true, SystemAdmin: true, Revision: 1, AuthVersion: 1}
	if err = tx.Create(&row).Error; err != nil {
		return err
	}
	return tx.Commit().Error
}
func (s *Store) ChangePassword(ctx context.Context, id string, authVersion int64, oldPassword, newPassword string) error {
	if err := s.writable(); err != nil {
		return err
	}
	if len(oldPassword) > 72 {
		return ErrUnauthorized
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	var row userRow
	if err := s.db.WithContext(ctx).Take(&row, "id = ?", id).Error; err != nil {
		return storageError(err)
	}
	if !row.Enabled || row.AuthVersion != authVersion || bcrypt.CompareHashAndPassword([]byte(row.PasswordHash), []byte(oldPassword)) != nil {
		return ErrUnauthorized
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Model(&userRow{}).Where("id = ? AND auth_version = ? AND password_hash = ? AND enabled = ?", id, authVersion, row.PasswordHash, true).Updates(map[string]any{"password_hash": string(hash), "auth_version": gorm.Expr("auth_version + 1"), "revision": gorm.Expr("revision + 1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return config.ErrConflict
	}
	return nil
}
