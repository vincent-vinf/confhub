package storage_test

import (
	"context"
	"fmt"
	"github.com/vincent-vinf/confhub/internal/clientinfo"
	"github.com/vincent-vinf/confhub/internal/testutil"
	"testing"
	"time"
)

func TestPresenceSharesClientsAndExpiresReplicaLeases(t *testing.T) {
	s := testutil.Store(t)
	ctx := context.Background()
	a := clientinfo.Client{ID: "a", Tags: map[string]string{"env": "gray", "sys.ip": "192.168.2.3"}, Subscriptions: []clientinfo.Subscription{}}
	b := clientinfo.Client{ID: "b", Tags: map[string]string{"env": "prod"}, Subscriptions: []clientinfo.Subscription{}}
	if err := s.SyncPresence(ctx, "node-a", []clientinfo.Client{a}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncPresence(ctx, "node-b", []clientinfo.Client{b}, 60*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	page, err := s.Clients(ctx, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Clients) != 1 || page.Clients[0].ID != "a" || page.NextAfter != "a" {
		t.Fatalf("page: %+v", page)
	}
	suggestions, err := s.TagSuggestions(ctx, "env", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(suggestions) != 2 || suggestions[0] != "gray" || suggestions[1] != "prod" {
		t.Fatal(suggestions)
	}
	time.Sleep(80 * time.Millisecond)
	page, err = s.Clients(ctx, "", 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Clients) != 1 || page.Clients[0].InstanceID != "node-a" {
		t.Fatalf("expired lease: %+v", page)
	}
	if err := s.SyncPresence(ctx, "node-a", nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	page, err = s.Clients(ctx, "", 25)
	if err != nil || len(page.Clients) != 0 {
		t.Fatalf("disconnect: %+v %v", page, err)
	}
	if err := s.RemovePresence(ctx, "node-a"); err != nil {
		t.Fatal(err)
	}
	names, err := s.TagSuggestions(ctx, "", "sys.")
	if err != nil || len(names) != 2 {
		t.Fatalf("built-ins: %v %v", names, err)
	}
}

func TestPresenceSuggestionsPreserveLiteralValuesAndReconcileUpdates(t *testing.T) {
	s := testutil.Store(t)
	ctx := context.Background()
	clients := []clientinfo.Client{
		{ID: "a", Tags: map[string]string{"env": "Gray", "rack": "%a", "space": "value "}},
		{ID: "b", Tags: map[string]string{"env": "gray", "rack": "other", "space": "value"}},
	}
	if err := s.SyncPresence(ctx, "node", clients, time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		tag, prefix string
		want        []string
	}{
		{"env", "G", []string{"Gray"}}, {"rack", "%", []string{"%a"}}, {"space", "", []string{"value", "value "}},
	} {
		got, err := s.TagSuggestions(ctx, tc.tag, tc.prefix)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s: %v", tc.tag, got)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatal(got, tc.want)
			}
		}
	}
	// Same connection changes version/tags; old suggestions must disappear.
	clients[0].Tags["env"] = "new"
	if err := s.SyncPresence(ctx, "node", clients[:1], time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := s.TagSuggestions(ctx, "env", "")
	if err != nil || len(got) != 1 || got[0] != "new" {
		t.Fatal(got, err)
	}
	page, err := s.Clients(ctx, "", 25)
	if err != nil || len(page.Clients) != 1 {
		t.Fatal(page, err)
	}
	// A rejected batch must not partially replace the good snapshot.
	if err := s.SyncPresence(ctx, "node", []clientinfo.Client{clients[0], clients[0]}, time.Minute); err == nil {
		t.Fatal("duplicate accepted")
	}
	page, err = s.Clients(ctx, "", 25)
	if err != nil || len(page.Clients) != 1 || page.Clients[0].Tags["env"] != "new" {
		t.Fatal(page, err)
	}
}

func TestTagNamesAreBoundedAndAlwaysIncludeBuiltIns(t *testing.T) {
	s := testutil.Store(t)
	ctx := context.Background()
	clients := []clientinfo.Client{}
	for i := 0; i < 3; i++ {
		tags := map[string]string{}
		for j := 0; j < 60; j++ {
			tags[fmt.Sprintf("a%03d", i*60+j)] = "v"
		}
		clients = append(clients, clientinfo.Client{ID: fmt.Sprintf("%d", i), Tags: tags})
	}
	if err := s.SyncPresence(ctx, "node", clients, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := s.TagSuggestions(ctx, "", "")
	if err != nil || len(got) != 100 {
		t.Fatal(got, err)
	}
	names := map[string]bool{}
	for _, name := range got {
		names[name] = true
	}
	if !names["sys.ip"] || !names["sys.hostname"] {
		t.Fatal("built-ins lost in bounded results")
	}
	got, err = s.TagSuggestions(ctx, "", "a179")
	if err != nil || len(got) != 1 || got[0] != "a179" {
		t.Fatal(got, err)
	}
}

func TestPresencePreservesNULTagsWithoutHidingHealthyClients(t *testing.T) {
	s := testutil.Store(t)
	ctx := context.Background()
	clients := []clientinfo.Client{
		{ID: "healthy", Tags: map[string]string{"env": "prod"}},
		{ID: "unusual", Tags: map[string]string{"odd\x00tag": "value\x00with-null", "emoji": "中文🟢"}},
	}
	if err := s.SyncPresence(ctx, "node", clients, time.Minute); err != nil {
		t.Fatal(err)
	}
	page, err := s.Clients(ctx, "", 25)
	if err != nil || len(page.Clients) != 2 {
		t.Fatal(page, err)
	}
	if page.Clients[1].Tags["odd\x00tag"] != "value\x00with-null" {
		t.Fatal(page.Clients[1])
	}
	values, err := s.TagSuggestions(ctx, "odd\x00tag", "value\x00")
	if err != nil || len(values) != 1 || values[0] != "value\x00with-null" {
		t.Fatal(values, err)
	}
	names, err := s.TagSuggestions(ctx, "", "odd\x00")
	if err != nil || len(names) != 1 || names[0] != "odd\x00tag" {
		t.Fatal(names, err)
	}
}
