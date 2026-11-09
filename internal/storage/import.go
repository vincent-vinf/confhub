package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/configarchive"
	"gorm.io/gorm"
)

type ImportEntry struct {
	Group            string `json:"group"`
	Name             string `json:"name"`
	ExpectedID       string `json:"expected_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Action           string `json:"action"`
	Error            string `json:"error,omitempty"`
}
type ImportPlan struct {
	Namespace     string        `json:"namespace"`
	Policy        string        `json:"policy"`
	Digest        string        `json:"digest"`
	Entries       []ImportEntry `json:"entries"`
	MissingGroups []string      `json:"missing_groups"`
	CanImport     bool          `json:"can_import"`
}
type ImportResult struct {
	Created   int   `json:"created"`
	Updated   int   `json:"updated"`
	Skipped   int   `json:"skipped"`
	Unchanged int   `json:"unchanged"`
	Sequence  int64 `json:"sequence"`
}

func validateImport(namespace, policy string, p configarchive.Package) error {
	if err := config.ValidateName(namespace); err != nil {
		return err
	}
	if policy != "ABORT" && policy != "SKIP" && policy != "OVERWRITE" {
		return fmt.Errorf("%w: invalid import policy", config.ErrInvalid)
	}
	return configarchive.Validate(p.Items)
}
func importGroup(ctx context.Context, tx *gorm.DB, namespace, group string, allowCreate bool) (bool, error) {
	var row groupRow
	err := tx.WithContext(ctx).Where(map[string]any{"namespace": namespace, "name": group}).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if !allowCreate {
			return false, fmt.Errorf("%w: group %q must be created by an administrator first", ErrForbidden, group)
		}
		return true, nil
	}
	return false, err
}

func (s *Store) PreviewImport(ctx context.Context, namespace, policy string, p configarchive.Package, allowCreate bool) (ImportPlan, error) {
	plan := ImportPlan{Namespace: namespace, Policy: policy, Digest: p.Digest, Entries: []ImportEntry{}, MissingGroups: []string{}, CanImport: true}
	validationErr := validateImport(namespace, policy, p)
	var problems *configarchive.ItemErrors
	if validationErr != nil && !errors.As(validationErr, &problems) {
		return plan, validationErr
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ns namespaceRow
		if err := tx.Take(&ns, "name = ?", namespace).Error; err != nil {
			return storageError(err)
		}
		groupErrors := map[string]error{}
		for i, item := range p.Items {
			entry := ImportEntry{Group: item.Group, Name: item.Name, Action: "create"}
			if problems != nil && problems.Messages[i] != "" {
				entry.Action, entry.Error = "error", problems.Messages[i]
				plan.CanImport = false
				plan.Entries = append(plan.Entries, entry)
				continue
			}
			if _, checked := groupErrors[item.Group]; !checked {
				missing, err := importGroup(ctx, tx, namespace, item.Group, allowCreate)
				if err != nil && !errors.Is(err, ErrForbidden) {
					return err
				}
				groupErrors[item.Group] = err
				if missing {
					plan.MissingGroups = append(plan.MissingGroups, item.Group)
				}
			}
			if err := groupErrors[item.Group]; err != nil {
				entry.Action, entry.Error = "error", err.Error()
				plan.CanImport = false
				plan.Entries = append(plan.Entries, entry)
				continue
			}
			state, err := s.load(ctx, tx, config.Key{Namespace: namespace, Group: item.Group, Name: item.Name})
			if err != nil && !errors.Is(err, config.ErrNotFound) {
				return err
			}
			if state != nil {
				entry.ExpectedID, entry.ExpectedRevision = state.ID, state.Revision
				switch policy {
				case "ABORT":
					entry.Action = "conflict"
					plan.CanImport = false
				case "SKIP":
					entry.Action = "skip"
				case "OVERWRITE":
					entry.Action = "overwrite"
					v := state.Versions[state.GlobalVersion]
					if v.Content == item.Content && v.Format == item.Format {
						entry.Action = "unchanged"
					}
				}
			}
			plan.Entries = append(plan.Entries, entry)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return plan, err
}

func (s *Store) Import(ctx context.Context, p configarchive.Package, plan ImportPlan, allowCreate bool) (ImportResult, error) {
	var result ImportResult
	if err := validateImport(plan.Namespace, plan.Policy, p); err != nil {
		return result, err
	}
	if plan.Digest != p.Digest || len(plan.Entries) != len(p.Items) {
		return result, fmt.Errorf("%w: preview does not match archive", config.ErrInvalid)
	}
	tx, seq, err := s.beginMutation(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var ns namespaceRow
	if err = tx.Take(&ns, "name = ?", plan.Namespace).Error; err != nil {
		return result, storageError(err)
	}
	groups := map[string]bool{}
	for i, item := range p.Items {
		entry := plan.Entries[i]
		if entry.Group != item.Group || entry.Name != item.Name {
			return ImportResult{}, fmt.Errorf("%w: preview item mismatch", config.ErrInvalid)
		}
		if plan.Policy == "SKIP" && entry.ExpectedID != "" {
			result.Skipped++
			continue
		}
		if plan.Policy == "ABORT" && entry.ExpectedID != "" {
			return ImportResult{}, fmt.Errorf("%w: configuration already exists: %s/%s", config.ErrConflict, item.Group, item.Name)
		}
		if !groups[item.Group] {
			missing, e := importGroup(ctx, tx, plan.Namespace, item.Group, allowCreate)
			if e != nil {
				return ImportResult{}, e
			}
			if missing {
				if e = tx.Create(&groupRow{Namespace: plan.Namespace, Name: item.Group}).Error; e != nil {
					return ImportResult{}, storageError(e)
				}
			}
			groups[item.Group] = true
		}
		k := config.Key{Namespace: plan.Namespace, Group: item.Group, Name: item.Name}
		m, e := s.saveInTransaction(ctx, tx, k, config.Edit{ExpectedID: entry.ExpectedID, ExpectedRevision: entry.ExpectedRevision, Content: item.Content, Format: item.Format, Description: item.Description})
		if e != nil {
			return ImportResult{}, fmt.Errorf("%s/%s: %w", item.Group, item.Name, e)
		}
		if !m.Changed {
			result.Unchanged++
			continue
		}
		if entry.ExpectedID == "" {
			result.Created++
		} else {
			result.Updated++
		}
		seq, e = s.appendEvent(ctx, tx, m.State, seq, false)
		if e != nil {
			return ImportResult{}, e
		}
	}
	if err = tx.Commit().Error; err != nil {
		return ImportResult{}, err
	}
	result.Sequence = seq
	return result, nil
}
