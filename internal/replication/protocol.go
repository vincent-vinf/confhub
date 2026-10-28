package replication

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"time"

	"bytes"
	"github.com/google/uuid"
	"github.com/vincent-vinf/confhub/internal/replication/wire"
	"google.golang.org/protobuf/proto"
)

const chunkBytes = 48 << 10
const chunkWindow = 8
const transferTimeout = 2 * time.Minute
const peerIdleTimeout = 15 * time.Second

type frame struct {
	Type      wire.FrameType
	RequestID string
	Cursor    Cursor
	Manifest  *Manifest
	Index     int
	Data      []byte
	Bytes     int64
	Digest    []byte
}

func cursorProto(c Cursor) *wire.Cursor { return &wire.Cursor{Source: c.Source, Sequence: c.Sequence} }
func cursorFromProto(c *wire.Cursor) Cursor {
	return Cursor{Source: c.GetSource(), Sequence: c.GetSequence()}
}
func send(ctx context.Context, c Conn, f frame) error {
	m := &wire.Frame{Version: ProtocolVersion, Type: f.Type, RequestId: f.RequestID, Cursor: cursorProto(f.Cursor),
		Index: int64(f.Index), Data: f.Data, Bytes: f.Bytes, Digest: f.Digest}
	if f.Manifest != nil {
		b := f.Manifest
		m.Manifest = &wire.Manifest{Cursor: cursorProto(b.Cursor), After: b.After, Full: b.Full, Count: int64(b.Count)}
	}
	data, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	if len(data) > MaxMessageBytes {
		return errors.New("replication frame exceeds limit")
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.Send(writeCtx, data)
}
func receive(ctx context.Context, c Conn) (frame, error) {
	data, err := c.Recv(ctx)
	if err != nil {
		return frame{}, err
	}
	if len(data) > MaxMessageBytes {
		return frame{}, errors.New("replication frame exceeds limit")
	}
	var m wire.Frame
	if err := (proto.UnmarshalOptions{RecursionLimit: 32}).Unmarshal(data, &m); err != nil {
		return frame{}, err
	}
	if m.Version != ProtocolVersion {
		return frame{}, errors.New("unsupported replication protocol")
	}
	if m.Index < 0 || m.Index > MaxTransferBytes || m.Bytes < 0 || m.Bytes > MaxTransferBytes {
		return frame{}, errors.New("invalid replication frame bounds")
	}
	f := frame{Type: m.Type, RequestID: m.RequestId, Cursor: cursorFromProto(m.Cursor), Index: int(m.Index), Data: m.Data, Bytes: m.Bytes, Digest: m.Digest}
	if b := m.Manifest; b != nil {
		if b.Count < 0 || b.Count > MaxRecords {
			return frame{}, errors.New("invalid replication record count")
		}
		f.Manifest = &Manifest{Cursor: cursorFromProto(b.Cursor), After: b.After, Full: b.Full, Count: int(b.Count)}
	}
	return f, nil
}

type limitedWriter struct {
	out     io.Writer
	written int64
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	if int64(len(data))+w.written > MaxTransferBytes {
		return 0, errors.New("replication transfer exceeds 1 GiB")
	}
	n, err := w.out.Write(data)
	w.written += int64(n)
	return n, err
}

// Serve runs a cloud session using the shared publisher.
// Notifications are hints. A periodic watermark also repairs a lost final hint.
func (p *Publisher) Serve(ctx context.Context, c Conn) error {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	messages := make(chan frame, 1)
	failures := make(chan error, 1)
	go func() {
		defer close(done)
		for {
			f, err := receive(ctx, c)
			if err != nil {
				failures <- err
				return
			}
			select {
			case messages <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); c.Close(); <-done }()
	heartbeat := time.NewTicker(2 * time.Second)
	defer heartbeat.Stop()
	var advertised Cursor
	var changes <-chan struct{}
	lastReceived := time.Now()
	notify := func(force bool) error {
		cursor, updated, err := p.watermark(ctx)
		changes = updated
		if err != nil {
			return err
		}
		if force || cursor != advertised {
			if err := send(ctx, c, frame{Type: wire.FrameType_FRAME_TYPE_WATERMARK, Cursor: cursor}); err != nil {
				return err
			}
			advertised = cursor
		}
		return nil
	}
	if err := notify(false); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-failures:
			return err
		case <-changes:
			if err := notify(false); err != nil {
				return err
			}
		case <-heartbeat.C:
			if time.Since(lastReceived) > peerIdleTimeout {
				return errors.New("replication peer heartbeat expired")
			}
			// Export can observe a newer cursor than our last poll; never send
			// the cached watermark after the edge has applied that snapshot.
			if err := notify(true); err != nil {
				return err
			}
		case f := <-messages:
			lastReceived = time.Now()
			switch f.Type {
			case wire.FrameType_FRAME_TYPE_FETCH:
				if _, err := uuid.Parse(f.RequestID); err != nil {
					return errors.New("invalid replication request identity")
				}
				ack := func(ctx context.Context, index int) error {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case err := <-failures:
						return err
					case response := <-messages:
						if response.Type != wire.FrameType_FRAME_TYPE_CHUNK_RECEIVED || response.RequestID != f.RequestID || response.Index != index {
							return errors.New("invalid replication window acknowledgement")
						}
						lastReceived = time.Now()
						return nil
					}
				}
				if err := transfer(ctx, c, p, f, ack); err != nil {
					return err
				}
				lastReceived = time.Now()
			case wire.FrameType_FRAME_TYPE_SEEN:
				// Receipt heartbeat only; never an applied cursor acknowledgement.
			case wire.FrameType_FRAME_TYPE_APPLIED:
				if f.Cursor.Source != advertised.Source || f.Cursor.Sequence > advertised.Sequence {
					if err := notify(false); err != nil {
						return err
					}
				}
			default:
				return errors.New("invalid replication request")
			}
		}
	}
}

func transfer(ctx context.Context, c Conn, source Source, request frame, ack func(context.Context, int) error) error {
	ctx, cancel := context.WithTimeout(ctx, transferTimeout)
	defer cancel()
	file, err := NewStagedFile()
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	writer := &limitedWriter{out: io.MultiWriter(file, hash)}
	batch, err := source.Export(ctx, request.Cursor, writer)
	if err != nil {
		return err
	}
	if batch.Count > MaxRecords {
		return errors.New("replication record limit exceeded")
	}
	if err := send(ctx, c, frame{Type: wire.FrameType_FRAME_TYPE_BEGIN, RequestID: request.RequestID, Manifest: &batch}); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	data := make([]byte, chunkBytes)
	index := 0
	for {
		n, err := file.Read(data)
		if n > 0 {
			if err := send(ctx, c, frame{Type: wire.FrameType_FRAME_TYPE_CHUNK, RequestID: request.RequestID, Index: index, Data: data[:n]}); err != nil {
				return err
			}
			index++
			if index%chunkWindow == 0 {
				if err := ack(ctx, index); err != nil {
					return err
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return send(ctx, c, frame{Type: wire.FrameType_FRAME_TYPE_END, RequestID: request.RequestID, Index: index, Bytes: writer.written, Digest: hash.Sum(nil)})
}

// Follow resumes only from the mirror's committed cursor. The callback observes
// cloud watermarks for status reporting, never changes persisted progress.
func Follow(ctx context.Context, c Conn, mirror Mirror, lease Lease, observed func(Cursor)) error {
	defer c.Close()
	status, err := mirror.MirrorStatus(ctx)
	if err != nil {
		return err
	}
	for {
		waitCtx, cancel := context.WithTimeout(ctx, peerIdleTimeout)
		f, err := receive(waitCtx, c)
		cancel()
		if err != nil {
			return err
		}
		if f.Type != wire.FrameType_FRAME_TYPE_WATERMARK || f.Cursor.Source == "" || f.Cursor.Sequence < 0 {
			return errors.New("invalid replication watermark")
		}
		if observed != nil {
			observed(f.Cursor)
		}
		if err := send(ctx, c, frame{Type: wire.FrameType_FRAME_TYPE_SEEN}); err != nil {
			return err
		}
		if status.Ready && (f.Cursor.Source != status.Source || f.Cursor.Sequence < status.Sequence) {
			return ErrSourceChanged
		}
		if status.Ready && f.Cursor.Sequence == status.Sequence {
			continue
		}
		request := frame{Type: wire.FrameType_FRAME_TYPE_FETCH, RequestID: uuid.NewString(), Cursor: status.Cursor}
		if !status.Ready {
			request.Cursor = Cursor{}
		}
		if err := send(ctx, c, request); err != nil {
			return err
		}
		batch, file, err := download(ctx, c, request.RequestID)
		if err != nil {
			return err
		}
		err = mirror.ApplyMirror(ctx, lease, batch, file)
		file.Close()
		if err != nil {
			return err
		}
		status, err = mirror.MirrorStatus(ctx)
		if err != nil {
			return err
		}
		if err := send(ctx, c, frame{Type: wire.FrameType_FRAME_TYPE_APPLIED, Cursor: status.Cursor}); err != nil {
			return err
		}
	}
}

func download(ctx context.Context, c Conn, id string) (_ Manifest, _ *StagedFile, err error) {
	ctx, cancel := context.WithTimeout(ctx, transferTimeout)
	defer cancel()
	file, err := NewStagedFile()
	if err != nil {
		return Manifest{}, nil, err
	}
	defer func() {
		if err != nil {
			file.Close()
		}
	}()
	var batch *Manifest
	index := 0
	var receivedBytes int64
	hash := sha256.New()
	for {
		f, err := receive(ctx, c)
		if err != nil {
			return Manifest{}, nil, err
		}
		if f.Type == wire.FrameType_FRAME_TYPE_WATERMARK {
			continue
		} // A hint may already be queued before fetch.
		if f.RequestID != id {
			return Manifest{}, nil, errors.New("replication request mismatch")
		}
		switch f.Type {
		case wire.FrameType_FRAME_TYPE_BEGIN:
			if batch != nil || f.Manifest == nil || f.Manifest.Source == "" || f.Manifest.Count < 0 || f.Manifest.Count > MaxRecords {
				return Manifest{}, nil, errors.New("invalid replication manifest")
			}
			batch = f.Manifest
		case wire.FrameType_FRAME_TYPE_CHUNK:
			if batch == nil || f.Index != index || len(f.Data) == 0 || len(f.Data) > chunkBytes || receivedBytes+int64(len(f.Data)) > MaxTransferBytes {
				return Manifest{}, nil, errors.New("invalid replication chunk")
			}
			if _, err := io.MultiWriter(file, hash).Write(f.Data); err != nil {
				return Manifest{}, nil, err
			}
			receivedBytes += int64(len(f.Data))
			index++
			if index%chunkWindow == 0 {
				if err := send(ctx, c, frame{Type: wire.FrameType_FRAME_TYPE_CHUNK_RECEIVED, RequestID: id, Index: index}); err != nil {
					return Manifest{}, nil, err
				}
			}
		case wire.FrameType_FRAME_TYPE_END:
			if batch == nil || f.Index != index || f.Bytes != receivedBytes || !bytes.Equal(f.Digest, hash.Sum(nil)) {
				return Manifest{}, nil, errors.New("incomplete or corrupt replication transfer")
			}
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				return Manifest{}, nil, err
			}
			return *batch, file, nil
		default:
			return Manifest{}, nil, errors.New("invalid replication transfer frame")
		}
	}
}
