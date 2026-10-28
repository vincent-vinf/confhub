package replication

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/vincent-vinf/confhub/internal/config"
)

const MaxTransferBytes = 1 << 30
const MaxRecordBytes = 16 << 20
const MaxRecords = 100000

// StagedFile removes its private temporary file when closed.
type StagedFile struct {
	*os.File
	unlinked bool
}

func NewStagedFile() (*StagedFile, error) {
	f, err := os.CreateTemp("", "confhub-replication-*")
	if err != nil {
		return nil, err
	}
	// An open file can be unlinked on Linux, so crashes release its disk space.
	// Other platforms fall back to removal on Close.
	unlinked := os.Remove(f.Name()) == nil
	return &StagedFile{File: f, unlinked: unlinked}, nil
}
func (f *StagedFile) Close() error {
	if f.unlinked {
		return f.File.Close()
	}
	return errors.Join(f.File.Close(), os.Remove(f.Name()))
}

// StageRecords validates a complete transfer before a database transaction starts.
func StageRecords(ctx context.Context, input io.Reader, count int, validate func(Record) error) (_ *StagedFile, err error) {
	if count < 0 || count > MaxRecords {
		return nil, errors.New("replication record limit exceeded")
	}
	f, err := NewStagedFile()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			f.Close()
		}
	}()
	limited := &io.LimitedReader{R: input, N: MaxTransferBytes + 1}
	decoder := NewRecordDecoder(io.TeeReader(limited, f))
	seen := map[config.Key]struct{}{}
	read := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var r Record
		decodeErr := decoder.Decode(&r)
		if MaxTransferBytes+1-limited.N > MaxTransferBytes {
			return nil, errors.New("replication transfer exceeds limit")
		}
		if decodeErr == io.EOF {
			break
		}
		if decodeErr != nil {
			return nil, decodeErr
		}
		if read >= count {
			return nil, errors.New("replication count mismatch")
		}
		if _, ok := seen[r.Key]; ok {
			return nil, errors.New("duplicate replication key")
		}
		seen[r.Key] = struct{}{}
		if err := validate(r); err != nil {
			return nil, err
		}
		read++
	}
	if read != count {
		return nil, errors.New("incomplete replication transfer")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return f, nil
}
