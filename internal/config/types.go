// Package config defines configuration identities, main versions and mutable gray content.
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("configuration changed; reload and confirm again")
	ErrInvalid  = errors.New("invalid input")
	ErrNotEmpty = errors.New("namespace or group is not empty")
)

const MaxContentBytes = 1 << 20

type Key struct {
	Namespace string `json:"namespace"`
	Group     string `json:"group"`
	Name      string `json:"name"`
}

func (k Key) Validate() error {
	for _, v := range []string{k.Namespace, k.Group, k.Name} {
		if err := ValidateName(v); err != nil {
			return err
		}
	}
	return nil
}
func ValidateName(v string) error {
	if !utf8.ValidString(v) || len(v) == 0 || len(v) > 128 || strings.TrimSpace(v) != v || strings.ContainsAny(v, "/\\\x00\r\n") {
		return fmt.Errorf("%w: names must contain 1–128 bytes without slashes or surrounding whitespace", ErrInvalid)
	}
	return nil
}

type Version struct {
	Number        int64     `json:"number"`
	Content       string    `json:"content"`
	Format        string    `json:"format"`
	Description   string    `json:"description"`
	Action        string    `json:"action"`
	SourceVersion int64     `json:"source_version,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	References    []string  `json:"references,omitempty"`
}
type Condition struct {
	Tag      string   `json:"tag"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}

// Beta is the mutable content owned by one rule. BaseVersion is a label,
// not a reference that pins a historical main version.
type Beta struct {
	BaseVersion int64  `json:"base_version"`
	Content     string `json:"content"`
	Format      string `json:"format"`
	Description string `json:"description"`
}
type RuleInput struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Enabled    bool        `json:"enabled"`
	Conditions []Condition `json:"conditions"`
}

func (r RuleInput) Rule() Rule {
	return Rule{ID: r.ID, Name: r.Name, Enabled: r.Enabled, Conditions: r.Conditions}
}

type Rule struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Enabled    bool        `json:"enabled"`
	Beta       Beta        `json:"beta"`
	Conditions []Condition `json:"conditions"`
}
type State struct {
	Sequence      int64             `json:"sequence"`
	ID            string            `json:"id"`
	Key           Key               `json:"key"`
	Revision      int64             `json:"revision"`
	LastVersion   int64             `json:"last_version"`
	GlobalVersion int64             `json:"global_version"`
	Rules         []Rule            `json:"rules"`
	Versions      map[int64]Version `json:"versions"`
}
type Effective struct {
	Sequence int64  `json:"sequence"`
	ID       string `json:"id,omitempty"`
	Key      Key    `json:"key"`
	Revision int64  `json:"revision"`
	Version  int64  `json:"version"`
	Content  string `json:"content"`
	Format   string `json:"format"`
	RuleID   string `json:"rule_id,omitempty"`
	Deleted  bool   `json:"deleted"`
}
type Edit struct {
	ExpectedID       string `json:"expected_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Content          string `json:"content"`
	Format           string `json:"format"`
	Description      string `json:"description"`
	RuleID           string `json:"rule_id,omitempty"`
}
type Mutation struct {
	State    *State `json:"state"`
	Changed  bool   `json:"changed"`
	Sequence int64  `json:"sequence"`
}
type Event struct {
	Sequence int64  `json:"sequence"`
	ID       string `json:"id"`
	Key      Key    `json:"key"`
	Deleted  bool   `json:"deleted"`
}
type Changes struct {
	Sequence      int64
	PurgedThrough int64
	Events        []Event
}
