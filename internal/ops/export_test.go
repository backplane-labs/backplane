package ops

import (
	"context"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	commonpb "go.temporal.io/api/common/v1"
	historypb "go.temporal.io/api/history/v1"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
)

// Internals under test.
var (
	MessagePB      = messagePB
	DeadLetterPB   = deadLetterPB
	EventFilter    = eventFilter
	ConsumerOfDead = consumerOfDead
	CallHook       = callHook
	RunActivity    = runActivity
)

// PayloadJSON exposes payloadJSON.
func PayloadJSON(p *commonpb.Payload) string { return payloadJSON(p) }

// HistoryPB exposes historyPB.
func HistoryPB(e *historypb.HistoryEvent) *consolev1.HistoryEvent { return historyPB(e) }

// RedriveMsg exposes redriveMsg for subscriber's reactor consumer of an
// event of source.
func RedriveMsg(source, subscriber, consumer string, m *jetstream.RawStreamMsg) *nats.Msg {
	return redriveMsg(reactor{subscriber: subscriber, consumer: consumer, source: source}, m)
}

// TestEvent exposes the message PublishTestEvent would send.
func (o *Ops) TestEvent(ctx context.Context, req *consolev1.PublishTestEventRequest) (*nats.Msg, error) {
	return EventAPI{o: o}.testEvent(ctx, req)
}

// ActivityRunOf exposes the input RunActivity would start.
func (o *Ops) ActivityRunOf(req *consolev1.RunActivityRequest) (ActivityRun, error) {
	return CallAPI{o: o}.activityRun(req)
}

// HookCallOf exposes the run CallHook would start.
func (o *Ops) HookCallOf(ctx context.Context, req *consolev1.CallHookRequest) (string, *backplanev1.HookCall, error) {
	opts, call, err := CallAPI{o: o}.hookCall(ctx, req)

	return opts.ID, call, err
}
