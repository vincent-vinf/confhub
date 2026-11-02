package replication

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
)

var (
	ErrSourceChanged = errors.New("replication source changed or moved backwards; explicitly rebuild the edge mirror")
	ErrLeaseLost     = errors.New("replication lease lost")
	ErrUninitialized = errors.New("edge mirror has no complete snapshot")
)

type Cursor struct {
	Source   string `json:"source"`
	Sequence int64  `json:"sequence"`
}
type Manifest struct {
	Cursor
	After int64 `json:"after"`
	Full  bool  `json:"full"`
	Count int   `json:"count"`
}

// Record represents a raw configuration aggregate, or an explicit deletion.
type Record struct {
	Key   config.Key    `json:"key"`
	State *config.State `json:"state,omitempty"`
}
type Lease struct {
	Owner string
	Epoch int64
}
type Status struct {
	Cursor
	Ready           bool      `json:"ready"`
	RebuildRequired bool      `json:"rebuild_required"`
	LastSuccess     time.Time `json:"last_success,omitempty"`
	Owner           string    `json:"sync_owner,omitempty"`
	LeaseExpires    time.Time `json:"lease_expires,omitempty"`
}
type Source interface {
	Watermark(context.Context) (Cursor, error)
	Export(context.Context, Cursor, io.Writer) (Manifest, error)
}
type Mirror interface {
	MirrorStatus(context.Context) (Status, error)
	AcquireMirrorLease(context.Context, string, time.Duration) (Lease, bool, error)
	ReleaseMirrorLease(context.Context, Lease) error
	RequireMirrorRebuild(context.Context, Lease) error
	ApplyMirror(context.Context, Lease, Manifest, io.Reader) error
}
