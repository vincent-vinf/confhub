package transport_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/replication/transport"
	"github.com/vincent-vinf/confhub/internal/replication/wire"
	"google.golang.org/protobuf/proto"
)

func TestNATSUsesVersionedProtobufHandshake(t *testing.T) {
	address := os.Getenv("CONFHUB_TEST_NATS_URL")
	if address == "" {
		t.Skip("requires test broker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	subject, token := "confhub.protobuf."+uuid.NewString(), "replication-test-01234567890123456789"
	done := make(chan error, 1)
	go func() {
		done <- transport.ListenNATS(ctx, address, subject, token, func(ctx context.Context, c replication.Conn) { <-ctx.Done() })
	}()
	defer func() { cancel(); <-done }()
	nc, err := nats.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	raw, err := proto.Marshal(&wire.OpenRequest{Version: replication.ProtocolVersion, Session: uuid.NewString(), Token: token})
	if err != nil {
		t.Fatal(err)
	}
	for {
		response, err := nc.RequestWithContext(ctx, subject+".open", raw)
		if errors.Is(err, nats.ErrNoResponders) {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var reply wire.OpenReply
		if err := proto.Unmarshal(response.Data, &reply); err != nil || reply.Version != replication.ProtocolVersion || !reply.Ok {
			t.Fatal("protobuf handshake rejected", &reply, err)
		}
		break
	}
	wrongVersion, _ := proto.Marshal(&wire.OpenRequest{Version: 1, Session: uuid.NewString(), Token: token})
	wrongToken, _ := proto.Marshal(&wire.OpenRequest{Version: replication.ProtocolVersion, Session: uuid.NewString(), Token: "wrong"})
	for _, invalid := range [][]byte{wrongVersion, wrongToken, {0xff}, []byte(`{"session":"old-json","token":"ignored"}`)} {
		response, err := nc.RequestWithContext(ctx, subject+".open", invalid)
		if err != nil {
			t.Fatal(err)
		}
		var reply wire.OpenReply
		if err := proto.Unmarshal(response.Data, &reply); err != nil || reply.Version != replication.ProtocolVersion || reply.Ok {
			t.Fatal("invalid handshake accepted", &reply, err)
		}
	}
	conn, err := transport.DialNATS(ctx, address, subject, token)
	if err != nil {
		t.Fatal("production dialer cannot open protobuf session", err)
	}
	conn.Close()
}
