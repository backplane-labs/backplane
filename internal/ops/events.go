package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/wire"
)

// backplaneService is the name backplane runs under: its consumers on a
// stream are rules (§8.1).
const backplaneService = "backplane"

// EventAPI is backplane.console.v1.EventService.
type EventAPI struct {
	consolev1.UnimplementedEventServiceServer

	o *Ops
}

var _ consolev1.EventServiceServer = EventAPI{}

// events is what ListEvents gathers: the declared events and the
// subscribers by full event name.
type events struct {
	byName  map[string]*consolev1.EventInfo
	streams map[string]*consolev1.StreamState
}

func (e *events) event(full string) *consolev1.EventInfo {
	if ev, ok := e.byName[full]; ok {
		return ev
	}

	service, name, _ := wire.SplitEvent(full)
	ev := &consolev1.EventInfo{Event: full, Service: service, Name: name, Subject: wire.Subject(service, name)}
	e.byName[full] = ev

	return ev
}

// ListEvents implements EventService.
func (a EventAPI) ListEvents(
	ctx context.Context, req *consolev1.ListEventsRequest,
) (*consolev1.ListEventsResponse, error) {
	listed, err := a.o.manifests(req.GetService())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	all, err := a.o.manifests("")
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	evs := &events{byName: map[string]*consolev1.EventInfo{}, streams: map[string]*consolev1.StreamState{}}
	sources := map[string]bool{}

	for _, m := range listed {
		sources[m.GetService()] = true

		for _, ev := range m.GetEvents() {
			info := evs.event(m.GetService() + "." + ev.GetName())
			info.Declared, info.Description, info.Schema = true, ev.GetDescription(), ev.GetSchema()
		}
	}

	// Reactors of every service on the listed events.
	for _, m := range all {
		for _, sub := range m.GetSubscriptions() {
			service, _, err := wire.SplitEvent(sub.GetEvent())
			if err != nil || (req.GetService() != "" && service != req.GetService()) {
				continue
			}

			sources[service] = true
			info := evs.event(sub.GetEvent())
			info.Subscribers = append(info.Subscribers, &consolev1.Subscriber{
				Kind: consolev1.SubscriberKind_SUBSCRIBER_KIND_REACTOR, Service: m.GetService(),
				Consumer: sub.GetConsumer(), Durable: wire.Durable(m.GetService(), sub.GetConsumer()), Event: sub.GetEvent(),
			})
		}
	}

	out := &consolev1.ListEventsResponse{}

	if jet, err := a.o.nats(); err != nil {
		out.NatsError = err.Error()
	} else if err := a.natsState(ctx, jet, evs, slices.Sorted(maps.Keys(sources))); err != nil {
		out.NatsError = err.Error()
	}

	for _, name := range slices.Sorted(maps.Keys(evs.byName)) {
		ev := evs.byName[name]
		slices.SortFunc(ev.GetSubscribers(), byDurable)
		out.Events = append(out.Events, ev)
	}

	out.Streams = evs.streams

	return out, nil
}

// byDurable orders subscribers by durable name.
func byDurable(x, y *consolev1.Subscriber) int {
	return strings.Compare(x.GetDurable(), y.GetDurable())
}

// natsState fills the counts, consumers and dead letters of the events of
// sources.
func (a EventAPI) natsState(ctx context.Context, jet jetstream.JetStream, evs *events, sources []string) error {
	for _, source := range sources {
		if err := sourceState(ctx, jet, evs, source); err != nil {
			return err
		}
	}

	return deadLetters(ctx, jet, evs)
}

// sourceState fills the stream, the message counts and the consumers of
// the events of source.
func sourceState(ctx context.Context, jet jetstream.JetStream, evs *events, source string) error {
	st, s, err := streamState(ctx, jet, wire.StreamName(source))
	if err != nil {
		return err
	}

	evs.streams[source] = st

	if s == nil {
		return nil
	}

	counts, err := subjectCounts(ctx, s, wire.StreamSubjects(source))
	if err != nil {
		return err
	}

	for _, event := range evs.byName {
		if event.GetService() != source {
			continue
		}

		if event.Messages = counts[event.GetSubject()]; event.GetMessages() > 0 {
			if m, err := s.GetLastMsgForSubject(ctx, event.GetSubject()); err == nil {
				event.LastSeq, event.LastTime = m.Sequence, stamp(m.Time)
			}
		}
	}

	cons, err := consumers(ctx, s)
	if err != nil {
		return err
	}

	for _, info := range cons {
		subscriberOf(evs, source, info)
	}

	return nil
}

// deadLetters fills the dead letters of the reactors.
func deadLetters(ctx context.Context, jet jetstream.JetStream, evs *events) error {
	subscribers := map[string]bool{}

	for _, sub := range reactors(evs) {
		subscribers[sub.GetService()] = true
	}

	for service := range subscribers {
		dead, err := deadCounts(ctx, jet, service)
		if err != nil {
			return err
		}

		for _, sub := range reactors(evs) {
			if sub.GetService() == service {
				sub.DeadLetters = dead[sub.GetConsumer()]
			}
		}
	}

	return nil
}

// reactors are the reactor subscribers of every event.
func reactors(evs *events) []*consolev1.Subscriber {
	var out []*consolev1.Subscriber

	for _, event := range evs.byName {
		for _, sub := range event.GetSubscribers() {
			if sub.GetKind() == consolev1.SubscriberKind_SUBSCRIBER_KIND_REACTOR {
				out = append(out, sub)
			}
		}
	}

	return out
}

// consumers of s with their state.
func consumers(ctx context.Context, s jetstream.Stream) ([]*jetstream.ConsumerInfo, error) {
	lister := s.ListConsumers(ctx)

	var out []*jetstream.ConsumerInfo
	for info := range lister.Info() {
		out = append(out, info)
	}

	if err := lister.Err(); err != nil {
		return out, fmt.Errorf("consumers of %s: %w", s.CachedInfo().Config.Name, err)
	}

	return out, nil
}

// subscriberOf finds the subscriber a consumer of source's stream is —
// a declared reactor by durable name, else by its metadata, a rule, or
// another consumer — and sets its state.
func subscriberOf(evs *events, source string, info *jetstream.ConsumerInfo) {
	for _, ev := range evs.byName {
		for _, s := range ev.GetSubscribers() {
			if s.GetDurable() == info.Name && ev.GetService() == source {
				s.State = consumerPB(info)
				s.Redrive = slices.Contains(filters(info.Config), wire.RedriveSubject(source, s.GetService(), s.GetConsumer()))

				return
			}
		}
	}

	sub := subscriberFor(source, info)

	if sub.GetEvent() != "" {
		ev := evs.event(sub.GetEvent())
		ev.Subscribers = append(ev.Subscribers, sub)
	}
}

// subscriberFor is the subscriber of a consumer from its name, metadata
// and filters.
func subscriberFor(source string, info *jetstream.ConsumerInfo) *consolev1.Subscriber {
	meta := info.Config.Metadata
	sub := &consolev1.Subscriber{
		Kind: consolev1.SubscriberKind_SUBSCRIBER_KIND_OTHER, Durable: info.Name, State: consumerPB(info),
		Event: meta[wire.MetaEvent],
	}

	switch {
	case strings.HasPrefix(info.Name, wire.Token(backplaneService)+"__"):
		sub.Kind, sub.Service = consolev1.SubscriberKind_SUBSCRIBER_KIND_RULE, backplaneService
		sub.Consumer = strings.TrimPrefix(info.Name, wire.Token(backplaneService)+"__")
	case meta[wire.MetaService] != "" && meta[wire.MetaConsumer] != "":
		sub.Kind = consolev1.SubscriberKind_SUBSCRIBER_KIND_REACTOR
		sub.Service, sub.Consumer = meta[wire.MetaService], meta[wire.MetaConsumer]
		sub.Redrive = slices.Contains(filters(info.Config), wire.RedriveSubject(source, sub.GetService(), sub.GetConsumer()))
	}

	if sub.GetEvent() == "" {
		for _, f := range filters(info.Config) {
			if ev, ok := wire.EventOfSubject(f); ok {
				sub.Event = ev

				break
			}
		}
	}

	return sub
}

// deadCounts are the dead letters per reactor consumer of subscriber.
func deadCounts(ctx context.Context, jet jetstream.JetStream, subscriber string) (map[string]uint64, error) {
	s, err := jet.Stream(ctx, wire.DLQStreamName(subscriber))
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		return map[string]uint64{}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("stream %s: %w", wire.DLQStreamName(subscriber), err)
	}

	counts, err := subjectCounts(ctx, s, wire.DLQStreamSubjects(subscriber))
	if err != nil {
		return nil, err
	}

	out := make(map[string]uint64, len(counts))

	for subject, n := range counts {
		if consumer, ok := consumerOfDead(subject); ok {
			out[consumer] += n
		}
	}

	return out, nil
}

// deadSubjectParts are the tokens of a dead-letter subject
// bp.dlq.<subscriber>.<consumer>.
const deadSubjectParts = 4

// consumerOfDead is the reactor consumer of a dead-letter subject
// bp.dlq.<subscriber>.<consumer>.
func consumerOfDead(subject string) (string, bool) {
	parts := strings.Split(subject, ".")
	if len(parts) != deadSubjectParts {
		return "", false
	}

	return wire.Untoken(parts[deadSubjectParts-1])
}

// GetStream implements EventService.
func (a EventAPI) GetStream(
	ctx context.Context, req *consolev1.GetStreamRequest,
) (*consolev1.GetStreamResponse, error) {
	if req.GetService() == "" {
		return nil, a.o.status(ctx, fmt.Errorf("%w: service is required", ErrInput))
	}

	jet, err := a.o.nats()
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	service := req.GetService()
	out := &consolev1.GetStreamResponse{Service: service}

	st, s, err := streamState(ctx, jet, wire.StreamName(service))
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	out.Events = st

	if s != nil {
		cons, err := consumers(ctx, s)
		if err != nil {
			return nil, a.o.status(ctx, err)
		}

		for _, info := range cons {
			out.Consumers = append(out.Consumers, subscriberFor(service, info))
		}

		slices.SortFunc(out.GetConsumers(), byDurable)
	}

	if out.DeadLetters, _, err = streamState(ctx, jet, wire.DLQStreamName(service)); err != nil {
		return nil, a.o.status(ctx, err)
	}

	dead, err := deadCounts(ctx, jet, service)
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	out.DeadLetterCounts = countsPB(dead)

	for _, c := range out.GetConsumers() {
		if c.GetService() == service {
			c.DeadLetters = dead[c.GetConsumer()]
		}
	}

	return out, nil
}

func countsPB(counts map[string]uint64) []*consolev1.DeadLetterCount {
	out := make([]*consolev1.DeadLetterCount, 0, len(counts))
	for _, c := range slices.Sorted(maps.Keys(counts)) {
		out = append(out, &consolev1.DeadLetterCount{Consumer: c, Count: counts[c]})
	}

	return out
}

// eventFilter is the subject filter of PeekMessages: one event (its
// CamelCase or full name) or every event of the service.
func eventFilter(service, event string) (string, error) {
	if event == "" {
		return "bp." + wire.Token(service) + ".*", nil
	}

	if s, name, ok := strings.Cut(event, "."); ok {
		if s != service {
			return "", fmt.Errorf("%w: event %q is not %s's", ErrInput, event, service)
		}

		event = name
	}

	if !camelName.MatchString(event) {
		return "", fmt.Errorf("%w: event %q", ErrInput, event)
	}

	return wire.Subject(service, event), nil
}

// PeekMessages implements EventService.
func (a EventAPI) PeekMessages(
	ctx context.Context, req *consolev1.PeekMessagesRequest,
) (*consolev1.PeekMessagesResponse, error) {
	if req.GetService() == "" {
		return nil, a.o.status(ctx, fmt.Errorf("%w: service is required", ErrInput))
	}

	filter, err := eventFilter(req.GetService(), req.GetEvent())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	jet, err := a.o.nats()
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	st, s, err := streamState(ctx, jet, wire.StreamName(req.GetService()))
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	out := &consolev1.PeekMessagesResponse{Stream: st}
	if s == nil {
		return out, nil
	}

	limit := pageSize(req.GetLimit())

	var msgs []*jetstream.RawStreamMsg

	switch {
	case req.GetStartSeq() > 0:
		msgs, out.NextSeq, err = forward(ctx, s, filter, req.GetStartSeq(), 0, limit)
	case req.GetStartTime() != nil:
		var seq uint64

		if seq, err = seqAt(ctx, jet, st.GetName(), filter, req.GetStartTime().AsTime()); err == nil && seq > 0 {
			msgs, out.NextSeq, err = forward(ctx, s, filter, seq, 0, limit)
		}
	default:
		msgs, err = tail(ctx, s, filter, st.GetFirstSeq(), st.GetLastSeq(), limit)
		if len(msgs) > 0 {
			out.NextSeq = msgs[len(msgs)-1].Sequence + 1
		}
	}

	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	for _, m := range msgs {
		out.Messages = append(out.Messages, messagePB(m))
	}

	return out, nil
}

// seqAt is the sequence of the first message matching filter at or after
// t; 0 when there is none.
func seqAt(ctx context.Context, jet jetstream.JetStream, stream, filter string, t time.Time) (uint64, error) {
	c, err := jet.OrderedConsumer(ctx, stream, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{filter}, DeliverPolicy: jetstream.DeliverByStartTimePolicy, OptStartTime: &t,
		InactiveThreshold: time.Minute,
	})
	if err != nil {
		return 0, fmt.Errorf("find %s at %s: %w", filter, t, err)
	}

	batch, err := c.FetchNoWait(1)
	if err != nil {
		return 0, fmt.Errorf("find %s at %s: %w", filter, t, err)
	}

	for m := range batch.Messages() {
		if md, err := m.Metadata(); err == nil {
			return md.Sequence.Stream, nil
		}
	}

	return 0, nil
}

var extName = regexp.MustCompile(`^[a-z0-9]+$`)

// reservedExt are the attributes an extension of a test event cannot
// name: the SDK's own and the console's marks.
//
//nolint:gochecknoglobals // constant set
var reservedExt = map[string]bool{
	"specversion": true, "id": true, "source": true, "type": true, "time": true, "subject": true,
	"datacontenttype": true, "dataschema": true, "instance": true, "version": true, "bptest": true, "bpauthor": true,
}

// testInstance is ce-instance of an event the console publishes.
const testInstance = "backplane"

// PublishTestEvent implements EventService. The event is the service's:
// ce-source is the service and ce-type its full name, as its emitter would
// publish it, so subscribers read it as any other; ce-instance is
// "backplane", ce-bptest "true" and ce-bpauthor the console session mark
// it as the console's.
func (a EventAPI) PublishTestEvent(
	ctx context.Context, req *consolev1.PublishTestEventRequest,
) (*consolev1.PublishTestEventResponse, error) {
	msg, err := a.testEvent(ctx, req)
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	jet, err := a.o.nats()
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	ack, err := jet.PublishMsg(ctx, msg)
	if errors.Is(err, jetstream.ErrNoStreamResponse) {
		service, _, _ := wire.SplitEvent(req.GetEvent())

		return nil, a.o.status(ctx, fmt.Errorf(
			"%w: stream %s does not exist yet (no emitter or subscriber of %s has started)",
			ErrPrecondition, wire.StreamName(service), service))
	}

	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	id := msg.Header.Get(wire.HeaderID)
	a.o.audit(ctx, "event.publish", req.GetEvent(), xlog.String("id", id), xlog.Uint64("seq", ack.Sequence))

	return &consolev1.PublishTestEventResponse{
		Id: id, Subject: msg.Subject, Seq: ack.Sequence, Duplicate: ack.Duplicate,
	}, nil
}

// testEvent is the message PublishTestEvent sends, checked.
func (a EventAPI) testEvent(ctx context.Context, req *consolev1.PublishTestEventRequest) (*nats.Msg, error) {
	service, name, err := splitFull("event", req.GetEvent())
	if err != nil {
		return nil, err
	}

	m, err := a.o.latest(service)
	if err != nil {
		return nil, err
	}

	if !slices.ContainsFunc(m.GetEvents(), func(ev *backplanev1.Event) bool { return ev.GetName() == name }) {
		return nil, fmt.Errorf("%w: %s does not declare event %s", ErrNotDeclared, service, name)
	}

	payload := []byte(req.GetPayload())
	if len(payload) == 0 {
		payload = []byte("{}")
	}

	if !json.Valid(payload) {
		return nil, fmt.Errorf("%w: payload is not valid JSON", ErrInput)
	}

	id := req.GetId()
	if id == "" {
		id = uuid.NewString()
	}

	h := nats.Header{}

	for k, v := range req.GetExtensions() {
		if !extName.MatchString(k) || reservedExt[k] || strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("%w: extension %q", ErrInput, k)
		}

		h.Set(wire.CEPrefix+k, v)
	}

	h.Set(wire.HeaderSpecVersion, wire.SpecVersion)
	h.Set(wire.HeaderID, id)
	h.Set(wire.HeaderSource, service)
	h.Set(wire.HeaderType, service+"."+name)
	h.Set(wire.HeaderTime, time.Now().UTC().Format(time.RFC3339Nano))
	h.Set(wire.HeaderContentType, wire.ContentJSON)
	h.Set(wire.HeaderMIME, wire.ContentJSON)
	h.Set(wire.HeaderInstance, testInstance)
	h.Set(wire.HeaderTest, "true")
	h.Set(wire.CEPrefix+"bpauthor", a.o.author(ctx))

	if req.GetKey() != "" {
		if strings.ContainsAny(req.GetKey(), "\r\n") {
			return nil, fmt.Errorf("%w: key has a line break", ErrInput)
		}

		h.Set(wire.HeaderSubject, req.GetKey())
	}

	h.Set(jetstream.MsgIDHeader, id)
	otel.GetTextMapPropagator().Inject(ctx, headerCarrier(h))

	return &nats.Msg{Subject: wire.Subject(service, name), Header: h, Data: payload}, nil
}

// headerCarrier adapts NATS headers to the OpenTelemetry propagator.
type headerCarrier nats.Header

func (c headerCarrier) Get(key string) string { return nats.Header(c).Get(key) }

func (c headerCarrier) Set(key, value string) { nats.Header(c).Set(key, value) }

func (c headerCarrier) Keys() []string { return slices.Collect(maps.Keys(c)) }
