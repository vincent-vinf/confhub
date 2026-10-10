package storage

import (
	"context"
	"errors"
	"fmt"

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
	var row adminRow
	err := s.db.WithContext(ctx).Take(&row, 1).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err = validatePassword(password); err != nil {
		return fmt.Errorf("first initialization requires admin-password: %w", err)
	}
	encoded, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, _, err := s.beginMutation(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	err = tx.Take(&row, 1).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err = tx.Create(&adminRow{ID: 1, PasswordHash: string(encoded)}).Error; err != nil {
		return err
	}
	return tx.Commit().Error
}
func (s *Store) Authenticate(ctx context.Context, username, password string) error {
	var row adminRow
	if err := s.db.WithContext(ctx).Take(&row, 1).Error; err != nil {
		return err
	}
	if len(password) > 72 {
		return ErrUnauthorized
	}
	err := bcrypt.CompareHashAndPassword([]byte(row.PasswordHash), []byte(password))
	if err != nil || username != "admin" {
		return ErrUnauthorized
	}
	return nil
}
func (s *Store) ChangePassword(ctx context.Context, oldPassword, newPassword string) error {
	if len(oldPassword) > 72 {
		return ErrUnauthorized
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	var row adminRow
	if err := s.db.WithContext(ctx).Take(&row, 1).Error; err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(row.PasswordHash), []byte(oldPassword)) != nil {
		return ErrUnauthorized
	}
	encoded, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Model(&adminRow{}).
		Where(map[string]any{"id": 1, "password_hash": row.PasswordHash}).Update("password_hash", string(encoded))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return config.ErrConflict
	}
	return nil
}
