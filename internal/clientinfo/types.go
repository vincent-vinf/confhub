// Package clientinfo defines read-only WebSocket connection metadata.
package clientinfo

import (
	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
	"time"
)

type Subscription struct {
	Key      config.Key `json:"key"`
	ID       string     `json:"id,omitempty"`
	Version  int64      `json:"version"`
	Revision int64      `json:"revision"`
	RuleID   string     `json:"rule_id,omitempty"`
	Deleted  bool       `json:"deleted"`
	Sent     bool       `json:"sent"`
}
type Client struct {
	ID            string            `json:"id"`
	InstanceID    string            `json:"instance_id"`
	SourceAddress string            `json:"source_address"`
	ConnectedAt   time.Time         `json:"connected_at"`
	RefreshedAt   time.Time         `json:"refreshed_at"`
	Tags          map[string]string `json:"tags"`
	Subscriptions []Subscription    `json:"subscriptions"`
}
type Page struct {
	Clients   []Client `json:"clients"`
	NextAfter string   `json:"next_after,omitempty"`
}
