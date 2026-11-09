package replication_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/replication"
)

func TestStagingAcceptsLengthDelimitedProtobufDeletion(t *testing.T) {
	// A literal protobuf record: key = {namespace: "public", group: "G", name: "a"}.
	// The first byte is the record's length; the missing state means deletion.
	raw := []byte{0x10, 0x0a, 0x0e, 0x0a, 0x06, 'p', 'u', 'b', 'l', 'i', 'c', 0x12, 0x01, 'G', 0x1a, 0x01, 'a'}
	calls := 0
	file, err := replication.StageRecords(context.Background(), bytes.NewReader(raw), 1, func(r replication.Record) error {
		calls++
		if r.Key != (config.Key{Namespace: "public", Group: "G", Name: "a"}) || r.State != nil {
			t.Fatalf("protobuf deletion changed meaning: %+v", r)
		}
		return nil
	})
	if err != nil {
		t.Fatal("valid protobuf record rejected", err)
	}
	defer file.Close()
	if calls != 1 {
		t.Fatal("record was not validated", calls)
	}
}

func TestProtobufRecordsPreserveMetadataAndLargeIntegers(t *testing.T) {
	k := config.Key{Namespace: "public", Group: "G", Name: "binary"}
	created := time.Date(2026, 10, 15, 1, 2, 3, 123456789, time.UTC)
	s := &config.State{Key: k, ID: "57b9b7d8-cb1d-4053-9d59-56ea40ce7948", Sequence: 9007199254740993,
		Revision: 9007199254740995, LastVersion: 1, GlobalVersion: 1,
		Versions: map[int64]config.Version{1: {Number: 1, Content: "中文\r\n\t\x00\\\"", Format: "text", Description: "主版本", Action: "rollback", SourceVersion: 7, CreatedAt: created, References: []string{"rule"}}},
		Beta:     &config.Beta{Content: "beta\n", Format: "text", Description: "灰度", UpdatedAt: created},
		Rules:    []config.Rule{{ID: "rule", Name: "范围", Enabled: true, Conditions: []config.Condition{{Tag: "sys.ip", Operator: "ip_range", Values: []string{"192.168.2.1", "192.168.2.5"}}}}}}
	var data bytes.Buffer
	if err := replication.NewRecordEncoder(&data).Encode(replication.Record{Key: k, State: s}); err != nil {
		t.Fatal(err)
	}
	file, err := replication.StageRecords(context.Background(), &data, 1, func(r replication.Record) error {
		v := r.State.Versions[1]
		if r.Key != k || r.State.Sequence != 9007199254740993 || r.State.Revision != 9007199254740995 || v.Content != "中文\r\n\t\x00\\\"" || !v.CreatedAt.Equal(created) || v.CreatedAt.Nanosecond() != 123456789 || v.SourceVersion != 7 || v.Action != "rollback" || v.Description != "主版本" || len(v.References) != 1 || v.References[0] != "rule" {
			t.Fatal("protobuf changed main configuration metadata", r)
		}
		if r.State.Beta == nil || r.State.Beta.Content != "beta\n" || r.State.Beta.Description != "灰度" || !r.State.Beta.UpdatedAt.Equal(created) || len(r.State.Rules) != 1 {
			t.Fatal("protobuf lost beta or rules", r)
		}
		rule := r.State.Rules[0]
		if rule.ID != "rule" || rule.Name != "范围" || !rule.Enabled || len(rule.Conditions) != 1 || strings.Join(rule.Conditions[0].Values, ",") != "192.168.2.1,192.168.2.5" || rule.Conditions[0].Tag != "sys.ip" || rule.Conditions[0].Operator != "ip_range" {
			t.Fatal("protobuf changed rule order or conditions", rule)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
}

func TestStagingRejectsMalformedProtobufLengths(t *testing.T) {
	for _, raw := range [][]byte{
		{0x80},                         // truncated length prefix
		bytes.Repeat([]byte{0xff}, 11), // overflowing length prefix
		binary.AppendUvarint(nil, replication.MaxRecordBytes+1), // reject before allocating
		{0x08, 0x0a},                             // truncated message
		{0x01, 0xff},                             // invalid protobuf field
		[]byte(`{"key":{"namespace":"public"}}`), // retired JSON format
	} {
		file, err := replication.StageRecords(context.Background(), bytes.NewReader(raw), 1, func(replication.Record) error { return nil })
		if file != nil {
			file.Close()
		}
		if err == nil {
			t.Fatalf("malformed record accepted: %x", raw)
		}
	}
}

// Protobuf omits empty repeated fields. Both transport decoding and persisted
// mirror decoding must still expose the HTTP contract rules:[], never null.
func TestProtobufEmptyRulesRemainJSONArray(t *testing.T) {
	for _, rules := range [][]config.Rule{nil, {}} {
		state := &config.State{ID: "no-rules", Key: config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "plain.yaml"},
			GlobalVersion: 1, Rules: rules, Versions: map[int64]config.Version{1: {Number: 1, Content: "key: value\n", Format: "yaml"}}}
		assertRules := func(t *testing.T, got *config.State) {
			t.Helper()
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err = json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["rules"]) != "[]" {
				t.Fatalf("empty rules must be JSON [], got %s", fields["rules"])
			}
		}
		t.Run("persisted", func(t *testing.T) {
			raw, err := replication.MarshalState(state)
			if err != nil {
				t.Fatal(err)
			}
			got, err := replication.UnmarshalState(raw)
			if err != nil {
				t.Fatal(err)
			}
			assertRules(t, got)
		})
		t.Run("transport", func(t *testing.T) {
			var data bytes.Buffer
			if err := replication.NewRecordEncoder(&data).Encode(replication.Record{Key: state.Key, State: state}); err != nil {
				t.Fatal(err)
			}
			var got replication.Record
			if err := replication.NewRecordDecoder(&data).Decode(&got); err != nil {
				t.Fatal(err)
			}
			assertRules(t, got.State)
		})
	}
}
