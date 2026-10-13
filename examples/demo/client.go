package demo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ws-proto/wsrpc"
)

var (
	ErrURL      = errors.New("console URL must be http(s)")
	ErrLogin    = errors.New("console login failed")
	ErrProtocol = errors.New("unexpected console response")
)

// Connect authenticates using a deployment-provided token, then opens the
// console's ws-proto transport using the returned HttpOnly session cookie.
// base includes the console prefix, e.g. http://localhost:8081/backplane.
func Connect(ctx context.Context, base, token string) (*wsrpc.ClientConn, error) {
	target, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return nil, fmt.Errorf("console URL: %w", err)
	}

	if target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return nil, ErrURL
	}

	origin := target.Scheme + "://" + target.Host

	payload, err := json.Marshal(struct {
		Token string `json:"token"`
	}{Token: token})
	if err != nil {
		return nil, fmt.Errorf("login payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String()+"/auth/login",
		strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("login request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d", ErrLogin, res.StatusCode)
	}

	headers := http.Header{"Origin": {origin}}

	for _, c := range res.Cookies() {
		if c.Name == "bp_session" {
			headers.Set("Cookie", c.Name+"="+c.Value)
		}
	}

	if headers.Get("Cookie") == "" {
		return nil, fmt.Errorf("%w: no session cookie", ErrLogin)
	}

	if target.Scheme == "https" {
		target.Scheme = "wss"
	} else {
		target.Scheme = "ws"
	}

	target.Path += "/ws"

	conn, err := wsrpc.Dial(ctx, target.String(), wsrpc.WithHeader(headers))
	if err != nil {
		return nil, fmt.Errorf("console websocket: %w", err)
	}

	return conn, nil
}

// Unary adapts a ws-proto connection to Install's call contract.
func Unary(conn *wsrpc.ClientConn) Call {
	return func(ctx context.Context, method string, req, res proto.Message) error {
		stream, err := conn.NewStream(ctx, method, nil)
		if err != nil {
			return fmt.Errorf("open %s: %w", method, err)
		}

		if err := stream.Send(req); err != nil {
			return fmt.Errorf("send %s: %w", method, err)
		}

		if err := stream.CloseSend(); err != nil {
			return fmt.Errorf("close send %s: %w", method, err)
		}

		if err := stream.Recv(res); err != nil {
			return fmt.Errorf("receive %s: %w", method, err)
		}

		if err := stream.Recv(res); !errors.Is(err, io.EOF) {
			return fmt.Errorf("end %s: expected EOF, got %w", method, err)
		}

		return nil
	}
}
