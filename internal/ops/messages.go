package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/redact"
	"github.com/backplane-labs/backplane/internal/wire"
)

// scanCap bounds the messages one page reads while it looks for the
// newest ones of a sparse subject.
const scanCap = 20000

// streamState is the state of stream name; exists is false when the
// server has no such stream.
//
//nolint:ireturn // Stream is only an interface
func streamState(
	ctx context.Context, jet jetstream.JetStream, name string,
) (*consolev1.StreamState, jetstream.Stream, error) {
	s, err := jet.Stream(ctx, name)
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		return &consolev1.StreamState{Name: name}, nil, nil
	}

	if err != nil {
		return nil, nil, fmt.Errorf("stream %s: %w", name, err)
	}

	info, err := s.Info(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("stream %s: %w", name, err)
	}

	return streamPB(info), s, nil
}

func streamPB(info *jetstream.StreamInfo) *consolev1.StreamState {
	cfg, st := info.Config, info.State

	return &consolev1.StreamState{
		Name: cfg.Name, Exists: true, Subjects: cfg.Subjects,
		Messages: st.Msgs, Bytes: st.Bytes,
		FirstSeq: st.FirstSeq, FirstTime: stamp(st.FirstTime), LastSeq: st.LastSeq, LastTime: stamp(st.LastTime),
		Consumers: uint32(max(st.Consumers, 0)), //nolint:gosec // a count
		MaxAge:    durationpb.New(cfg.MaxAge), MaxBytes: cfg.MaxBytes,
		Replicas:    int32(cfg.Replicas), //nolint:gosec // small
		DedupWindow: durationpb.New(cfg.Duplicates),
		EnsuredBy:   cfg.Metadata[wire.MetaBy], Kind: cfg.Metadata[wire.MetaKind],
	}
}

// subjectCounts are the messages per subject of s under filter.
func subjectCounts(ctx context.Context, s jetstream.Stream, filter string) (map[string]uint64, error) {
	info, err := s.Info(ctx, jetstream.WithSubjectFilter(filter))
	if err != nil {
		return nil, fmt.Errorf("stream subjects %s: %w", filter, err)
	}

	return info.State.Subjects, nil
}

// consumerPB is the state of a consumer.
func consumerPB(info *jetstream.ConsumerInfo) *consolev1.ConsumerState {
	cfg := info.Config

	filters := cfg.FilterSubjects
	if cfg.FilterSubject != "" {
		filters = append([]string{cfg.FilterSubject}, filters...)
	}

	return &consolev1.ConsumerState{
		NumPending: info.NumPending, NumAckPending: uint64(max(info.NumAckPending, 0)),
		NumRedelivered: uint64(max(info.NumRedelivered, 0)), NumWaiting: uint64(max(info.NumWaiting, 0)),
		DeliveredSeq: info.Delivered.Stream, AckFloorSeq: info.AckFloor.Stream,
		LastDelivered: stampPtr(info.Delivered.Last), LastAcked: stampPtr(info.AckFloor.Last),
		Paused: info.Paused, Created: stamp(info.Created), FilterSubjects: filters,
		MaxDeliver: int64(cfg.MaxDeliver), MaxAckPending: int64(cfg.MaxAckPending),
		AckWait: durationpb.New(cfg.AckWait), DeliverPolicy: cfg.DeliverPolicy.String(), Metadata: cfg.Metadata,
	}
}

// filters of a consumer, FilterSubject and FilterSubjects together.
func filters(cfg jetstream.ConsumerConfig) []string {
	if cfg.FilterSubject != "" {
		return append([]string{cfg.FilterSubject}, cfg.FilterSubjects...)
	}

	return cfg.FilterSubjects
}

// messagePB is one stored message as the console shows it, its payload
// masked by the event's schema in cat.
func messagePB(m *jetstream.RawStreamMsg, cat redact.Catalog) *consolev1.EventMessage {
	out := &consolev1.EventMessage{
		Seq: m.Sequence, Subject: m.Subject, StoredAt: stamp(m.Time),
		Headers: firstValues(m.Header), CloudEvent: cloudEventPB(m.Header),
		Test: m.Header.Get(wire.HeaderTest) == "true",
	}

	if ev, ok := wire.EventOfSubject(m.Subject); ok {
		out.Event = ev
	} else {
		out.Event = m.Header.Get(wire.HeaderType)
	}

	if json.Valid(m.Data) {
		out.Data = string(redact.Mask(redact.EventSchema(cat, out.GetEvent()), m.Data))
	} else {
		out.Raw = m.Data
	}

	return out
}

func firstValues(h nats.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}

	out := make(map[string]string, len(h))
	for k, vs := range h {
		if len(vs) > 0 {
			out[k] = vs[0]
		}
	}

	return out
}

// core are the CloudEvents context attributes and the SDK's own
// extensions, not listed among the extensions.
//
//nolint:gochecknoglobals // constant set
var core = map[string]bool{
	"specversion": true, "id": true, "source": true, "type": true, "time": true, "subject": true,
	"datacontenttype": true, "dataschema": true, "instance": true, "version": true,
}

func cloudEventPB(h nats.Header) *consolev1.CloudEvent {
	if h.Get(wire.HeaderSpecVersion) == "" && h.Get(wire.HeaderType) == "" {
		return nil
	}

	out := &consolev1.CloudEvent{
		Id: h.Get(wire.HeaderID), Source: h.Get(wire.HeaderSource), Type: h.Get(wire.HeaderType),
		Subject: h.Get(wire.HeaderSubject), Instance: h.Get(wire.HeaderInstance), Version: h.Get(wire.HeaderVersion),
	}

	if t, err := time.Parse(time.RFC3339Nano, h.Get(wire.HeaderTime)); err == nil {
		out.Time = timestamppb.New(t)
	}

	for k := range h {
		if len(k) <= len(wire.CEPrefix) || !strings.EqualFold(k[:len(wire.CEPrefix)], wire.CEPrefix) {
			continue
		}

		name := strings.ToLower(k[len(wire.CEPrefix):])
		if core[name] {
			continue
		}

		if out.Extensions == nil {
			out.Extensions = map[string]string{}
		}

		out.Extensions[name] = h.Get(k)
	}

	return out
}

// deadLetterPB is a dead letter with the reasons its headers carry.
func deadLetterPB(m *jetstream.RawStreamMsg, cat redact.Catalog) *consolev1.DeadLetter {
	delivered, _ := strconv.ParseUint(m.Header.Get(wire.HeaderDelivered), 10, 32)
	out := &consolev1.DeadLetter{
		Message: messagePB(m, cat), Consumer: m.Header.Get(wire.HeaderConsumer), Error: m.Header.Get(wire.HeaderError),
		Delivered: uint32(delivered),
	}

	if service, name, err := wire.SplitEvent(m.Header.Get(wire.HeaderType)); err == nil {
		out.EventSubject = wire.Subject(service, name)
	}

	return out
}

// forward reads the messages of s matching filter from seq on, at most
// limit of them and none above last (0: no bound). next is the sequence
// to continue at, 0 when nothing more matched.
func forward(
	ctx context.Context, s jetstream.Stream, filter string, seq, last uint64, limit int,
) ([]*jetstream.RawStreamMsg, uint64, error) {
	var out []*jetstream.RawStreamMsg

	for seq = max(seq, 1); len(out) < limit; {
		m, err := s.GetMsg(ctx, seq, jetstream.WithGetMsgSubject(filter))
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			return out, 0, nil
		}

		if err != nil {
			return out, 0, fmt.Errorf("get message %d: %w", seq, err)
		}

		if last > 0 && m.Sequence > last {
			return out, 0, nil
		}

		out = append(out, m)
		seq = m.Sequence + 1
	}

	return out, seq, nil
}

// tail reads the newest limit messages of s matching filter in
// [first, last], oldest first: it looks back through windows growing
// fourfold until one holds limit messages or starts at first, reading at
// most scanCap messages in all.
func tail(
	ctx context.Context, s jetstream.Stream, filter string, first, last uint64, limit int,
) ([]*jetstream.RawStreamMsg, error) {
	if last == 0 || last < first {
		return nil, nil
	}

	first = max(first, 1)
	read := 0

	for w := uint64(max(limit, 1)); ; w *= 4 {
		from := first
		if last-first+1 > w {
			from = last - w + 1
		}

		msgs, _, err := forward(ctx, s, filter, from, last, scanCap-read)
		if err != nil {
			return nil, err
		}

		read += len(msgs)

		if len(msgs) >= limit || from == first || read >= scanCap {
			return msgs[max(len(msgs)-limit, 0):], nil
		}
	}
}

// stamp is t as a timestamp; unset for the zero time and for the Unix
// epoch, which Temporal's SDK reports for a time the server left out.
func stamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() || t.Equal(time.Unix(0, 0)) {
		return nil
	}

	return timestamppb.New(t)
}

func stampPtr(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}

	return stamp(*t)
}
