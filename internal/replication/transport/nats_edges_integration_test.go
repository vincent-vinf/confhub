package transport_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/replication/transport"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestNATSIndependentEdgesBothReceiveSameCloudPublication(t *testing.T) {
	address := os.Getenv("CONFHUB_TEST_NATS_URL")
	if address == "" {
		t.Skip("set CONFHUB_TEST_NATS_URL for real broker integration")
	}
	cloud := testutil.Store(t)
	edges := []*storage.Store{testutil.Store(t), testutil.Store(t)}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	subject := "confhub.multiedge." + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	token := "replication-test-01234567890123456789"
	listenerDone := make(chan error, 1)
	go func() {
		listenerDone <- transport.ListenNATS(ctx, address, subject, token, func(ctx context.Context, c replication.Conn) {
			_ = serve(ctx, c, cloud, 10*time.Millisecond)
		})
	}()
	defer func() { cancel(); <-listenerDone }()
	followers := make(chan error, 2)
	for _, edge := range edges {
		var conn replication.Conn
		for {
			dialCtx, stop := context.WithTimeout(ctx, time.Second)
			c, err := transport.DialNATS(dialCtx, address, subject, token)
			stop()
			if err == nil {
				conn = c
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(err)
			case <-time.After(10 * time.Millisecond):
			}
		}
		lease, _, err := edge.AcquireMirrorLease(ctx, "owner", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		go func(edge *storage.Store, c replication.Conn, lease replication.Lease) {
			followers <- replication.Follow(ctx, c, edge, lease, nil)
		}(edge, conn, lease)
	}
	wait := func(condition func() bool) {
		for !condition() {
			select {
			case err := <-followers:
				t.Fatal("edge session stopped", err)
			case <-ctx.Done():
				t.Fatal("edge did not converge")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	for _, edge := range edges {
		wait(func() bool { s, err := edge.MirrorStatus(ctx); return err == nil && s.Ready })
	}
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "broadcast"}
	if _, err := cloud.Save(ctx, key, config.Edit{Content: "every edge", Format: "text"}); err != nil {
		t.Fatal(err)
	}
	for _, edge := range edges {
		wait(func() bool {
			s, _, err := edge.MirrorCurrent(ctx, key)
			return err == nil && s.Versions[1].Content == "every edge"
		})
	}
	cancel()
	for range edges {
		<-followers
	}
}
