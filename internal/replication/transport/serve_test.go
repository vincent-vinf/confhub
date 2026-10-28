package transport_test

import (
	"context"
	"github.com/vincent-vinf/confhub/internal/replication"
	"time"
)

func serve(ctx context.Context, c replication.Conn, source replication.Source, poll time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	publisher := replication.NewPublisher(source, poll)
	done := make(chan struct{})
	go func() { defer close(done); publisher.Run(ctx) }()
	defer func() { cancel(); <-done }()
	return publisher.Serve(ctx, c)
}
