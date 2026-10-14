package direct_test

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/direct"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/wire"
)

// activityServer is a service's ActivityService on a real port: it
// answers with what fn says and records the secret it was shown.
type activityServer struct {
	backplanev1.UnimplementedActivityServiceServer

	fn     func(call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error)
	secret atomic.Pointer[string]
	calls  atomic.Int32
}

func (s *activityServer) Run(
	ctx context.Context, req *backplanev1.RunRequest,
) (*backplanev1.RunResponse, error) {
	s.calls.Add(1)

	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("bp-internal-secret"); len(v) == 1 {
		s.secret.Store(&v[0])
	}

	res, err := s.fn(req.GetCall())
	if err != nil {
		return nil, err
	}

	return &backplanev1.RunResponse{Result: res}, nil
}

// serve starts an ActivityService and is its host and port.
func serve(t *testing.T, s *activityServer) (string, uint32) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	g := grpc.NewServer()
	backplanev1.RegisterActivityServiceServer(g, s)

	go func() { _ = g.Serve(ln) }()

	t.Cleanup(g.Stop)

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)

	return host, uint32(p)
}

// instance is a serving, registered, healthy instance of service at
// host:port running version 1.0.0.
func instance(id, service, host string, port uint32, healthy bool) registry.Instance {
	return registry.Instance{
		ID: id, Registered: true, Healthy: healthy, Address: host, Port: port,
		State: &backplanev1.InstanceState{
			Id: id, Service: service, Version: "1.0.0", Address: host, PlatformPort: port,
			Phase: backplanev1.InstancePhase_INSTANCE_PHASE_SERVING,
		},
	}
}

func publish(hub *registry.Hub, instances ...registry.Instance) {
	services := map[string]registry.Service{}

	for _, m := range installation() {
		services[m.GetService()] = registry.Service{
			Name: m.GetService(), Manifests: map[string]*backplanev1.Manifest{m.GetVersion(): m},
		}
	}

	for _, in := range instances {
		svc := services[in.State.GetService()]
		svc.Instances = append(svc.Instances, in)
		services[in.State.GetService()] = svc
	}

	hub.Publish(services)
}

// A call reaches an instance whose manifest declares the activity, with
// the internal secret; an unreachable instance is skipped for another; a
// handler's failure comes back with its detail; without an instance the
// call fails retryably.
func TestActivities(t *testing.T) {
	t.Parallel()

	ok := &activityServer{fn: func(call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
		return &backplanev1.ActivityResult{Payload: []byte(`{"echo":` + string(call.GetPayload()) + `}`)}, nil
	}}
	host, port := serve(t, ok)

	hub := registry.NewHub()
	// A dead address (nothing listens) and the live one, both healthy.
	publish(hub, instance("smtp-dead", "smtp", "127.0.0.1", 1, true), instance("smtp-1", "smtp", host, port, true))

	acts := direct.NewActivities(hub, "s3cret")
	t.Cleanup(acts.Close)

	call := &backplanev1.ActivityCall{Activity: "smtp.Send", Payload: []byte(`{"to":"a"}`), Step: "send"}

	res, err := acts.Call(t.Context(), call, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if string(res.GetPayload()) != `{"echo":{"to":"a"}}` || ok.secret.Load() == nil || *ok.secret.Load() != "s3cret" {
		t.Fatalf("result %s secret %v", res.GetPayload(), ok.secret.Load())
	}

	// No instance declares template.Exec on smtp's manifest: not an
	// instance of template at all.
	_, err = acts.Call(t.Context(), &backplanev1.ActivityCall{Activity: "template.Exec"}, time.Second)

	var f *direct.Failure
	if !errors.As(err, &f) || !f.Retryable || f.Type != wire.StepFailedType {
		t.Fatalf("no instance: %#v", err)
	}

	// A handler failure: the status and its detail.
	bad := &activityServer{fn: func(*backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
		st, _ := status.New(codes.FailedPrecondition, "billing.Charge: declined").WithDetails(
			&backplanev1.CallFailure{Type: wire.NonRetryableType, Retryable: false})

		return nil, st.Err()
	}}
	bhost, bport := serve(t, bad)
	publish(hub, instance("smtp-1", "smtp", host, port, true), instance("billing-1", "billing", bhost, bport, true))

	_, err = acts.Call(t.Context(), &backplanev1.ActivityCall{Activity: "billing.Charge", Payload: []byte(`{}`)}, time.Second)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("handler failure: %v", err)
	}

	// An unhealthy instance is used only when no healthy one is there.
	publish(hub, instance("smtp-1", "smtp", host, port, false))

	if _, err := acts.Call(t.Context(), call, time.Second); err != nil {
		t.Fatalf("fallback: %v", err)
	}

	// A timeout bounds the call.
	slow := &activityServer{fn: func(*backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
		time.Sleep(300 * time.Millisecond)

		return &backplanev1.ActivityResult{}, nil
	}}
	shost, sport := serve(t, slow)
	publish(hub, instance("smtp-slow", "smtp", shost, sport, true))

	_, err = acts.Call(t.Context(), call, 50*time.Millisecond)
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("timeout: %v", err)
	}

	// Prune drops connections to instances that left; calls still work
	// for the ones present.
	publish(hub, instance("smtp-1", "smtp", host, port, true))
	acts.Prune()

	if _, err := acts.Call(t.Context(), call, time.Second); err != nil {
		t.Fatalf("after prune: %v", err)
	}
}
