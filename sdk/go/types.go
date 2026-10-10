// Package confhub retrieves and watches raw ConfHub configuration snapshots.
package confhub

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// JSON can expand one raw UTF-8 byte into a six-byte escape. Leave room for
// metadata while still enforcing the protocol's decoded one-MiB content limit.
const maxWireBytes = 8 << 20

var (
	ErrNotFound    = errors.New("confhub: configuration not found")
	ErrUnavailable = errors.New("confhub: no available server or cached configuration")
	ErrClosed      = errors.New("confhub: client closed")
)

type Key struct {
	Namespace string `json:"namespace"`
	Group     string `json:"group"`
	Name      string `json:"name"`
}

func (k Key) normalized() (Key, error) {
	if k.Namespace == "" {
		k.Namespace = "public"
	}
	if k.Group == "" {
		k.Group = "DEFAULT_GROUP"
	}
	for _, value := range []string{k.Namespace, k.Group, k.Name} {
		if !utf8.ValidString(value) || len(value) == 0 || len(value) > 128 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "/\\\x00\r\n") {
			return k, fmt.Errorf("confhub: invalid configuration key")
		}
	}
	return k, nil
}

type Source string

const (
	Online Source = "online"
	Memory Source = "memory"
	Disk   Source = "disk"
)

// Snapshot contains unmodified configuration text. Deleted snapshots occur in
// callbacks; Get returns them together with ErrNotFound.
type Snapshot struct {
	Sequence int64  `json:"sequence"`
	ID       string `json:"id,omitempty"`
	Key      Key    `json:"key"`
	Revision int64  `json:"revision"`
	Version  int64  `json:"version"`
	Content  string `json:"content"`
	Format   string `json:"format"`
	RuleID   string `json:"rule_id,omitempty"`
	Deleted  bool   `json:"deleted"`
	Source   Source `json:"-"`
}

func (s Snapshot) valid(key Key) bool {
	return s.Key == key && s.Sequence >= 0 && s.Revision >= 0 && len(s.Content) <= 1<<20 && utf8.ValidString(s.Content) && (s.Deleted || (s.ID != "" && s.Version > 0 && s.Format != ""))
}
