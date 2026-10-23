package temporal_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	inftemporal "github.com/backplane-labs/backplane/pkg/backplane/infra/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal/temporaltest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
	"github.com/backplane-labs/backplane/pkg/backplane/meta"
)

// step plays a binding step with a heartbeat timeout: a workflow with id
// wfID executes the activity by name on the service's queue as activity
// "a1".
func step(t *testing.T, svc, name, wfID string, heartbeat time.Duration) (*backplanev1.ActivityResult, error) {
	t.Helper()

	tc := temporaltest.Dial(t)
	queue := "wf-" + svc
	run := func(ctx workflow.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			ActivityID:          "a1",
			TaskQueue:           svc,
			StartToCloseTimeout: 10 * time.Second,
			HeartbeatTimeout:    heartbeat,
			RetryPolicy:         &sdktemporal.RetryPolicy{InitialInterval: 50 * time.Millisecond, MaximumAttempts: 3},
		})

		var res backplanev1.ActivityResult

		err := workflow.ExecuteActivity(ctx, call.GetActivity(), call).Get(ctx, &res)

		return &res, err
	}

	temporaltest.Serve(t, tc, queue, func(w worker.Worker) {
		w.RegisterWorkflowWithOptions(run, workflow.RegisterOptions{Name: "step"})
	})

	ctx, cancel := context.WithTimeout(t.Context(), callLimit)
	defer cancel()

	r, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: wfID, TaskQueue: queue}, "step",
		&backplanev1.ActivityCall{
			Activity: name, Payload: []byte(`{}`), Binding: svc + ".Hook", Step: "render",
			Metadata: map[string]string{"user": "u-1"},
		})
	if err != nil {
		t.Fatal(err)
	}

	var res backplanev1.ActivityResult

	err = r.Get(ctx, &res)

	return &res, err
}

// The handler's ctx carries the execution's info; Heartbeat keeps a step
// with a short heartbeat timeout alive; a panic is retried.
func TestActivityInfoHeartbeatPanic(t *testing.T) {
	t.Parallel()

	svc := temporaltest.Name("info")

	var (
		seen   atomic.Pointer[env.ActivityInfo]
		user   atomic.Pointer[string]
		panics atomic.Int32
	)

	service(t, svc, func(e *env.Env) {
		e.Activity("Info", func(ctx context.Context, _ []byte) ([]byte, error) {
			info, ok := env.ActivityInfoOf(ctx)
			if !ok {
				return nil, env.NonRetryableError{Err: errors.New("no info")}
			}

			seen.Store(&info)

			u := meta.Get(ctx, "user")
			user.Store(&u)

			return []byte(`{}`), nil
		})
		e.Activity("Beat", func(ctx context.Context, _ []byte) ([]byte, error) {
			info, _ := env.ActivityInfoOf(ctx)

			for range 8 {
				time.Sleep(150 * time.Millisecond)
				info.Heartbeat("progress")
			}

			return []byte(`{"beat":true}`), nil
		})
		e.Activity("Panic", func(context.Context, []byte) ([]byte, error) {
			if panics.Add(1) == 1 {
				panic("boom")
			}

			return []byte(`{"ok":true}`), nil
		})
	})

	wfID := svc + "-wf"
	if _, err := step(t, svc, "Info", wfID, 0); err != nil {
		t.Fatal(err)
	}

	info := seen.Load()
	if info == nil || info.Attempt != 1 || info.Binding != svc+".Hook" || info.Step != "render" ||
		info.Key != wfID+"/a1" || info.Deadline.IsZero() || time.Until(info.Deadline) > 10*time.Second {
		t.Fatalf("info: %+v", info)
	}

	if u := user.Load(); u == nil || *u != "u-1" {
		t.Fatalf("metadata: %v", u)
	}

	// 1.2s of work under a 500ms heartbeat timeout.
	if res, err := step(t, svc, "Beat", svc+"-beat", 500*time.Millisecond); err != nil ||
		string(res.GetPayload()) != `{"beat":true}` {
		t.Fatalf("heartbeat: %s %v", res.GetPayload(), err)
	}

	if res, err := step(t, svc, "Panic", svc+"-panic", 0); err != nil ||
		string(res.GetPayload()) != `{"ok":true}` || panics.Load() != 2 {
		t.Fatalf("panic retried: %s %v (%d)", res.GetPayload(), err, panics.Load())
	}
}

func selfSigned(t *testing.T) (string, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw}))
}

func newClient(t *testing.T, p temporal.Params) *temporal.Client {
	t.Helper()

	p.Service, p.Log = "svc", testlog.Discard()
	p.Env = env.New("svc", manifest.New("svc", "0.0.0"))

	return temporal.New(p)
}

func TestOptions(t *testing.T) {
	t.Parallel()

	cert, key := selfSigned(t)

	// Plain: no TLS, no credentials, default dial timeout.
	c := newClient(t, temporal.Params{Conn: inftemporal.Config{Addr: "h:7233"}})

	opts, err := c.Options()
	if err != nil || opts.ConnectionOptions.TLS != nil || opts.Credentials != nil || opts.HostPort != "h:7233" ||
		opts.Namespace != "default" || c.DialTimeout() != 2*time.Second {
		t.Fatalf("plain: %+v %v", opts, err)
	}

	// mTLS with a private CA.
	c = newClient(t, temporal.Params{
		Conn: inftemporal.Config{
			Addr: "h:7233", DialTimeout: 5 * time.Second,
			TLS: config.TLS{Enabled: true, CA: cert, Cert: cert, Key: config.Secret(key), ServerName: "temporal.internal"},
		},
	})

	opts, err = c.Options()
	if err != nil {
		t.Fatal(err)
	}

	tls := opts.ConnectionOptions.TLS
	if tls == nil || tls.RootCAs == nil || len(tls.Certificates) != 1 || tls.ServerName != "temporal.internal" ||
		tls.InsecureSkipVerify || c.DialTimeout() != 5*time.Second {
		t.Fatalf("mtls: %+v", tls)
	}

	// API key: credentials; TLS comes with them (the SDK turns it on).
	c = newClient(t, temporal.Params{Conn: inftemporal.Config{Addr: "ns.tmprl.cloud:7233", APIKey: "secret"}})

	if opts, err = c.Options(); err != nil || opts.Credentials == nil {
		t.Fatalf("api key: %+v %v", opts, err)
	}

	// Broken PEM: an error, at Connect too.
	for _, bad := range []config.TLS{
		{Enabled: true, CA: "not a pem"},
		{Enabled: true, Cert: cert},
		{Enabled: true, Cert: cert, Key: "junk"},
	} {
		c = newClient(t, temporal.Params{Conn: inftemporal.Config{Addr: "h:7233", TLS: bad}})
		if _, err := c.Options(); err == nil {
			t.Errorf("accepted %+v", bad)
		}

		if err := c.Connect(t.Context(), temporaltest.NewGroup(t)); err == nil || !strings.Contains(err.Error(), "tls") {
			t.Errorf("connect with %+v: %v", bad, err)
		}
	}

	// Disabled TLS ignores its fields.
	c = newClient(t, temporal.Params{Conn: inftemporal.Config{Addr: "h:7233", TLS: config.TLS{CA: "junk"}}})
	if opts, err = c.Options(); err != nil || opts.ConnectionOptions.TLS != nil {
		t.Fatalf("disabled: %v", err)
	}
}

// New installs the platform hook timeout on the env for workflows.CallHook.
func TestHookTimeoutInstalled(t *testing.T) {
	t.Parallel()

	e := env.New("svc", manifest.New("svc", "0.0.0"))
	if e.HookTimeout() != env.DefaultHookTimeout {
		t.Fatalf("default %v", e.HookTimeout())
	}

	temporal.New(temporal.Params{Service: "svc", Env: e, HookTimeout: 7 * time.Second, Log: testlog.Discard()})

	if e.HookTimeout() != 7*time.Second {
		t.Fatalf("installed %v", e.HookTimeout())
	}
}

func TestCheckName(t *testing.T) {
	t.Parallel()

	for _, ok := range []string{"Send", "SendEmail", "V2", "A1b"} {
		temporal.CheckName("hook", ok)
	}

	for _, bad := range []string{"", "send", "Send_Email", "Send-Email", "Send.Email", "1Send", "Отправить"} {
		func() {
			defer func() {
				if r, _ := recover().(string); !strings.Contains(r, "hook name "+`"`+bad+`"`+" is not CamelCase") {
					t.Errorf("%q: panic %q", bad, r)
				}
			}()

			temporal.CheckName("hook", bad)
		}()
	}
}
