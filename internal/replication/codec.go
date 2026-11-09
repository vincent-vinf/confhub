package replication

import (
	"bufio"
	"errors"
	"io"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/replication/wire"
	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ProtocolVersion separates the protobuf protocol from the retired JSON v1.
const ProtocolVersion = 2

// RecordEncoder writes bounded, varint length-delimited protobuf records.
type RecordEncoder struct{ out io.Writer }

func NewRecordEncoder(out io.Writer) *RecordEncoder { return &RecordEncoder{out: out} }
func (e *RecordEncoder) Encode(r Record) error {
	m := &wire.Record{Key: keyProto(r.Key), State: stateProto(r.State)}
	if proto.Size(m) > MaxRecordBytes {
		return errors.New("replication record exceeds limit")
	}
	_, err := (protodelim.MarshalOptions{MarshalOptions: proto.MarshalOptions{Deterministic: true}}).MarshalTo(e.out, m)
	return err
}

// RecordDecoder checks the announced size before allocating the message body.
type RecordDecoder struct{ in *bufio.Reader }

func NewRecordDecoder(in io.Reader) *RecordDecoder {
	return &RecordDecoder{in: bufio.NewReader(in)}
}
func (d *RecordDecoder) Decode(r *Record) error {
	var m wire.Record
	options := protodelim.UnmarshalOptions{MaxSize: MaxRecordBytes, UnmarshalOptions: proto.UnmarshalOptions{RecursionLimit: 64}}
	if err := options.UnmarshalFrom(d.in, &m); err != nil {
		return err
	}
	s, err := stateFromProto(m.State)
	if err != nil {
		return err
	}
	*r = Record{Key: keyFromProto(m.Key), State: s}
	return nil
}

// MarshalState and UnmarshalState use the same schema for edge persistence and
// transfer records. Configuration text stays verbatim inside string fields.
func MarshalState(s *config.State) ([]byte, error) {
	if s == nil {
		return nil, errors.New("missing replication state")
	}
	m := stateProto(s)
	if proto.Size(m) > MaxRecordBytes {
		return nil, errors.New("replication state exceeds limit")
	}
	return (proto.MarshalOptions{Deterministic: true}).Marshal(m)
}
func UnmarshalState(data []byte) (*config.State, error) {
	if len(data) == 0 || len(data) > MaxRecordBytes {
		return nil, errors.New("invalid replication state size")
	}
	var m wire.State
	if err := (proto.UnmarshalOptions{RecursionLimit: 64}).Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return stateFromProto(&m)
}

func keyProto(k config.Key) *wire.Key {
	return &wire.Key{Namespace: k.Namespace, Group: k.Group, Name: k.Name}
}
func keyFromProto(k *wire.Key) config.Key {
	return config.Key{Namespace: k.GetNamespace(), Group: k.GetGroup(), Name: k.GetName()}
}
func timestampProto(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
func timestampFromProto(t *timestamppb.Timestamp) (time.Time, error) {
	if t == nil {
		return time.Time{}, nil
	}
	if err := t.CheckValid(); err != nil {
		return time.Time{}, err
	}
	return t.AsTime(), nil
}
func stateProto(s *config.State) *wire.State {
	if s == nil {
		return nil
	}
	m := &wire.State{Sequence: s.Sequence, Id: s.ID, Key: keyProto(s.Key), Revision: s.Revision,
		LastVersion: s.LastVersion, GlobalVersion: s.GlobalVersion, Versions: map[int64]*wire.Version{}}
	for n, v := range s.Versions {
		m.Versions[n] = &wire.Version{Number: v.Number, Content: v.Content, Format: v.Format, Description: v.Description,
			Action: v.Action, SourceVersion: v.SourceVersion, CreatedAt: timestampProto(v.CreatedAt), References: v.References}
	}
	for _, r := range s.Rules {
		rule := &wire.Rule{Id: r.ID, Name: r.Name, Enabled: r.Enabled}
		for _, c := range r.Conditions {
			rule.Conditions = append(rule.Conditions, &wire.Condition{Tag: c.Tag, Operator: c.Operator, Values: c.Values})
		}
		m.Rules = append(m.Rules, rule)
	}
	if s.Beta != nil {
		b := s.Beta
		m.Beta = &wire.Beta{Content: b.Content, Format: b.Format, Description: b.Description, UpdatedAt: timestampProto(b.UpdatedAt)}
	}
	return m
}
func stateFromProto(m *wire.State) (*config.State, error) {
	if m == nil {
		return nil, nil
	}
	s := &config.State{Sequence: m.Sequence, ID: m.Id, Key: keyFromProto(m.Key), Revision: m.Revision,
		LastVersion: m.LastVersion, GlobalVersion: m.GlobalVersion, Versions: map[int64]config.Version{},
		Rules: make([]config.Rule, 0, len(m.Rules))}
	for n, v := range m.Versions {
		if v == nil {
			return nil, errors.New("missing replication version")
		}
		created, err := timestampFromProto(v.CreatedAt)
		if err != nil {
			return nil, err
		}
		s.Versions[n] = config.Version{Number: v.Number, Content: v.Content, Format: v.Format, Description: v.Description,
			Action: v.Action, SourceVersion: v.SourceVersion, CreatedAt: created, References: v.References}
	}
	for _, r := range m.Rules {
		rule := config.Rule{ID: r.GetId(), Name: r.GetName(), Enabled: r.GetEnabled()}
		for _, c := range r.GetConditions() {
			rule.Conditions = append(rule.Conditions, config.Condition{Tag: c.GetTag(), Operator: c.GetOperator(), Values: c.GetValues()})
		}
		s.Rules = append(s.Rules, rule)
	}
	if m.Beta != nil {
		b := m.Beta
		updated, err := timestampFromProto(b.UpdatedAt)
		if err != nil {
			return nil, err
		}
		s.Beta = &config.Beta{Content: b.Content, Format: b.Format, Description: b.Description, UpdatedAt: updated}
	}
	return s, nil
}
