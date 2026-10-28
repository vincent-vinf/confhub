package replication_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/vincent-vinf/confhub/internal/replication"
)

func TestStagingRejectsIncompleteAndDuplicateRecords(t *testing.T) {
	valid := []byte{0x10, 0x0a, 0x0e, 0x0a, 0x06, 'p', 'u', 'b', 'l', 'i', 'c', 0x12, 0x01, 'G', 0x1a, 0x01, 'a'}
	for _, tc := range []struct {
		body  []byte
		count int
	}{{valid, 2}, {append(append([]byte{}, valid...), valid...), 2}, {valid, 0}, {[]byte{0x05, 0x0a}, 1}} {
		file, err := replication.StageRecords(context.Background(), bytes.NewReader(tc.body), tc.count, func(replication.Record) error { return nil })
		if file != nil {
			file.Close()
		}
		if err == nil {
			t.Fatal("invalid transfer accepted", tc)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if file, err := replication.StageRecords(ctx, bytes.NewReader(valid), 1, func(replication.Record) error { return nil }); err == nil {
		file.Close()
		t.Fatal("canceled transfer accepted")
	}
}
