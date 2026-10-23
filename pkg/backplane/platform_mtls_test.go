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
	hv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/activity"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/guard"
)

type echoIn struct {
	Text string `json:"text"`
}

// With backplane.mesh.platform=mtls the platform port serves the probes
// plain and everything else to a service certificate only; a service dials
// it with PlatformCredentials and PlatformTLS.
func TestPlatformMutualTLS(t *testing.T) {
	addr := os.Getenv("BACKPLANE_TEST_CONSUL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set")
	}

	const server, client = "svc-platform-a", "svc-platform-b"

	c, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		for _, name := range []string{server, client} {
			_, _ = c.KV().DeleteTree("backplane/services/"+name+"/", nil)
			_ = c.Agent().ServiceDeregister(name + "-1")
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	serverPorts := setup(t, addr)
	t.Setenv("BACKPLANE_MESH_PLATFORM", "mtls")

	a, err := backplane.Open(t.Context(), func(root backplane.Root[testConfig]) (*testState, error) {
		activity.Handle(root, "Echo", func(_ context.Context, in echoIn) (echoIn, error) { return in, nil })

		return &testState{}, nil
	}, baseOptions(server)...)
	if err != nil {
		t.Fatal(err)
	}

	run(ctx, a)
	eventually(t, "server ready", ready(serverPorts))

	platform := "127.0.0.1:" + serverPorts.platform
	withSecret := metadata.AppendToOutgoingContext(ctx, guard.Header, secret)
	echo := func(conn *grpc.ClientConn) error {
		cctx, ccancel := context.WithTimeout(withSecret, 3*time.Second)
		defer ccancel()

		_, err := backplanev1.NewActivityServiceClient(conn).Run(cctx, &backplanev1.RunRequest{
			Call: &backplanev1.ActivityCall{Activity: server + ".Echo", Payload: []byte(`{"text":"hi"}`)},
		})

		return err
	}

	// Plain: the probes answer, the rest does not.
	plain, err := grpc.NewClient(platform, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()

	if _, err := hv1.NewHealthClient(plain).Check(ctx, &hv1.HealthCheckRequest{}); err != nil {
		t.Fatalf("plain gRPC health: %v", err)
	}

	if err := echo(plain); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("plain activity call: %v", err)
	}

	get := func(client *http.Client, url string) int {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
		req.Header.Set(guard.HTTPHeader, secret)

		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}

		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()

		return res.StatusCode
	}

	if code := get(http.DefaultClient, "http://"+platform+"/healthz/liveness"); code != http.StatusOK {
		t.Fatalf("plain probe: %d", code)
	}

	if code := get(http.DefaultClient, "http://"+platform+"/_backplane/info"); code != http.StatusUnauthorized {
		t.Fatalf("plain info: %d", code)
	}

	// Another service over mutual TLS.
	setup(t, addr)
	t.Setenv("BACKPLANE_MESH_PLATFORM", "mtls")

	b, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions(client)...)
	if err != nil {
		t.Fatal(err)
	}

	run(ctx, b)

	mutual, err := grpc.NewClient(platform, grpc.WithTransportCredentials(b.PlatformCredentials(server)))
	if err != nil {
		t.Fatal(err)
	}
	defer mutual.Close()

	eventually(t, "b calls a's activity over mTLS", func() bool { return echo(mutual) == nil })

	tlsClient := &http.Client{Transport: &http.Transport{TLSClientConfig: b.PlatformTLS(server)}, Timeout: 3 * time.Second}
	if code := get(tlsClient, "https://"+platform+"/_backplane/info"); code != http.StatusOK {
		t.Fatalf("info over mTLS: %d", code)
	}

	if strings.TrimSpace(b.Name()) != client {
		t.Fatal("unexpected client")
	}
}

// mesh.platform other than plain needs the mesh: Open fails without it.
func TestPlatformMutualTLSNeedsTheMesh(t *testing.T) {
	setup(t, "")
	t.Setenv("BACKPLANE_MESH_PLATFORM", "accept")

	_, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("svc-platform-no-mesh")...)
	if err == nil || !strings.Contains(err.Error(), "mesh.platform=accept") {
		t.Fatalf("open: %v", err)
	}

	t.Setenv("BACKPLANE_MESH_PLATFORM", "tls")

	_, err = backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("svc-platform-bad")...)
	if err == nil || !strings.Contains(err.Error(), "mesh.platform must be") {
		t.Fatalf("open: %v", err)
	}
}
