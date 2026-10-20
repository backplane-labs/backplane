package backplane_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	hellov1 "github.com/backplane-labs/backplane/examples/hello/proto/hello/v1"
	"github.com/backplane-labs/backplane/pkg/backplane"
	bpapi "github.com/backplane-labs/backplane/pkg/backplane/api"
)

// whoGreets greets with the name of the service that called.
type whoGreets struct {
	hellov1.UnimplementedHelloServiceServer
}

func (whoGreets) Greet(ctx context.Context, _ *hellov1.GreetRequest) (*hellov1.GreetResponse, error) {
	caller, ok := backplane.CallerOf(ctx)
	if !ok {
		return nil, status.Error(codes.Internal, "no caller")
	}

	return &hellov1.GreetResponse{Greeting: "hi " + caller.Service}, nil
}

// Two services over Consul's CA: a serves gRPC and HTTP with api.MTLS, b
// calls it with its credentials; intentions decide who may.
//
//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestMeshMutualTLS(t *testing.T) {
	addr := os.Getenv("BACKPLANE_TEST_CONSUL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set")
	}

	const server, client = "svc-mesh-a", "svc-mesh-b"

	c, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, _ = c.ConfigEntries().Delete(api.ServiceIntentions, server, nil)

		for _, name := range []string{server, client} {
			_, _ = c.KV().DeleteTree("backplane/services/"+name+"/", nil)
			_ = c.Agent().ServiceDeregister(name + "-1")
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	serverPorts := setup(t, addr)

	a, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions(server)...)
	if err != nil {
		t.Fatal(err)
	}

	a.GRPC(func(r grpc.ServiceRegistrar) { hellov1.RegisterHelloServiceServer(r, whoGreets{}) }, bpapi.MTLS())
	a.HTTP("/who", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, _ := backplane.CallerOf(r.Context())
		_, _ = io.WriteString(w, caller.Service)
	}), bpapi.MTLS())

	run(ctx, a)
	eventually(t, "server ready", ready(serverPorts))

	setup(t, addr)

	var b *backplane.Service[testState]

	b, err = backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions(client)...)
	if err != nil {
		t.Fatal(err)
	}

	conn, err := grpc.NewClient("127.0.0.1:"+serverPorts.public, grpc.WithTransportCredentials(b.ServiceCredentials(server)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	run(ctx, b)

	greet := func() (string, error) {
		cctx, ccancel := context.WithTimeout(ctx, 3*time.Second)
		defer ccancel()

		out, err := hellov1.NewHelloServiceClient(conn).Greet(cctx, &hellov1.GreetRequest{Name: "x"})

		return out.GetGreeting(), err
	}

	eventually(t, "b greets a over mTLS", func() bool { g, err := greet(); return err == nil && g == "hi "+client })

	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: b.ServiceTLS(server)}, Timeout: 3 * time.Second}

	res, err := httpClient.Get("https://127.0.0.1:" + serverPorts.public + "/who")
	if err != nil {
		t.Fatal(err)
	}

	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if res.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != client {
		t.Fatalf("http: %d %q", res.StatusCode, body)
	}

	// Without a certificate the port does not answer.
	plain, err := grpc.NewClient("127.0.0.1:"+serverPorts.public, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()

	pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
	defer pcancel()

	if _, err := hellov1.NewHelloServiceClient(plain).Greet(pctx, &hellov1.GreetRequest{}); err == nil {
		t.Fatal("a plain client got through")
	}

	// An intention denies b: the next call is refused, existing connection or not.
	if _, _, err := c.ConfigEntries().Set(&api.ServiceIntentionsConfigEntry{
		Kind: api.ServiceIntentions, Name: server,
		Sources: []*api.SourceIntention{{Name: client, Action: api.IntentionActionDeny}},
	}, nil); err != nil {
		t.Fatal(err)
	}

	eventually(t, "denied", func() bool { _, err := greet(); return status.Code(err) == codes.PermissionDenied })

	if _, err := c.ConfigEntries().Delete(api.ServiceIntentions, server, nil); err != nil {
		t.Fatal(err)
	}

	eventually(t, "allowed again", func() bool { _, err := greet(); return err == nil })
}

//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestMTLSNeedsTheMesh(t *testing.T) {
	setup(t, "")

	svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("svc-no-mesh")...)
	if err != nil {
		t.Fatal(err)
	}

	svc.GRPC(func(r grpc.ServiceRegistrar) { hellov1.RegisterHelloServiceServer(r, whoGreets{}) }, bpapi.MTLS())

	if err := svc.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "mutual TLS needs") {
		t.Fatalf("run: %v", err)
	}
}

//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestMTLSAndPlainRoutesOnOnePort(t *testing.T) {
	addr := os.Getenv("BACKPLANE_TEST_CONSUL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set")
	}

	setup(t, addr)

	svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("svc-mixed-tls")...)
	if err != nil {
		t.Fatal(err)
	}

	svc.GRPC(func(r grpc.ServiceRegistrar) { hellov1.RegisterHelloServiceServer(r, whoGreets{}) }, bpapi.MTLS())
	svc.HTTP("/plain", http.NotFoundHandler())

	if err := svc.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "api.MTLS and plain routes") {
		t.Fatalf("run: %v", err)
	}
}
