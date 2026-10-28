package replication_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/replication"
)

func TestCommunicationPreservesMessagesAndCancellation(t *testing.T) {
	a, b := replication.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	sent := make(chan error, 1)
	go func() { sent <- a.Send(ctx, []byte("first")) }()
	got, err := b.Recv(ctx)
	if err != nil || string(got) != "first" {
		t.Fatalf("message: %q %v", got, err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.Recv(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Recv(ctx); err == nil {
		t.Fatal("peer close must unblock receive")
	}
	if err := a.Close(); err != nil {
		t.Fatal("close must be idempotent", err)
	}
}
