package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
	"golang.org/x/crypto/bcrypt"
)

var ErrUnauthorized = errors.New("invalid credentials")

func validatePassword(password string) error {
	if len(password) < 8 || len(password) > 72 {
		return fmt.Errorf("%w: password must contain 8–72 bytes", config.ErrInvalid)
	}
	return nil
}
func (s *Store) InitializeAdmin(ctx context.Context, password string) error {
	var hash string
	err := s.db.QueryRowContext(ctx, "SELECT password_hash FROM admin WHERE id=1").Scan(&hash)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
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
	err = tx.QueryRowContext(ctx, "SELECT password_hash FROM admin WHERE id=1").Scan(&hash)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.query("INSERT INTO admin(id,password_hash) VALUES (1,?)"), string(encoded)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Authenticate(ctx context.Context, username, password string) error {
	var hash string
	if err := s.db.QueryRowContext(ctx, "SELECT password_hash FROM admin WHERE id=1").Scan(&hash); err != nil {
		return err
	}
	if len(password) > 72 {
		return ErrUnauthorized
	}
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err != nil || username != "admin" {
		return ErrUnauthorized
	}
	return nil
}
func (s *Store) ChangePassword(ctx context.Context, oldPassword, newPassword string) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	var hash string
	if err := s.db.QueryRowContext(ctx, "SELECT password_hash FROM admin WHERE id=1").Scan(&hash); err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(oldPassword)) != nil {
		return ErrUnauthorized
	}
	encoded, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, s.query("UPDATE admin SET password_hash=? WHERE id=1 AND password_hash=?"), string(encoded), hash)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return config.ErrConflict
	}
	return nil
}
