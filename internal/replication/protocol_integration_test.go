package replication_test

import (
	"context"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestProtocolMirrorsMainBetaAndRuleChanges(t *testing.T) {
	cloud, edge := testutil.Store(t), testutil.Store(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "protocol"}
	main, err := cloud.Save(ctx, key, config.Edit{Content: "main", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := edge.AcquireMirrorLease(ctx, "worker", time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	a, b := replication.Pipe()
	defer a.Close()
	defer b.Close()
	errors := make(chan error, 2)
	go func() { errors <- serve(ctx, a, cloud, 20*time.Millisecond) }()
	go func() { errors <- replication.Follow(ctx, b, edge, lease, nil) }()
	wait := func(content string, beta bool) {
		t.Helper()
		for {
			state, _, err := edge.MirrorCurrent(ctx, key)
			if err == nil {
				effective := config.Resolve(state, map[string]string{"env": "test"})
				if effective.Content == content && effective.Beta == beta && state.GlobalVersion == 1 {
					return
				}
			}
			select {
			case err := <-errors:
				t.Fatal("protocol stopped", err)
			case <-ctx.Done():
				t.Fatal("mirror did not converge")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	wait("main", false)
	gray, err := cloud.SetRules(ctx, key, main.State.ID, main.State.Revision, []config.Rule{{ID: "test", Name: "test", Enabled: true, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"test"}}}}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cloud.Save(ctx, key, config.Edit{ExpectedID: gray.State.ID, ExpectedRevision: gray.State.Revision, Target: "beta", Content: "beta", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	wait("beta", true)
}
