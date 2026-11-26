package api_test

import (
	"context"
	"testing"

	"github.com/gopherex/ws-proto/wsrpc"

	"github.com/backplane-labs/backplane/pkg/backplane/api"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
)

func BenchmarkUnknownHandler(b *testing.B) {
	handler := func(context.Context, *wsrpc.Stream) error { return nil }

	b.ReportAllocs()

	for b.Loop() {
		_ = link.APIs.WS([]api.WSProtoOption{api.UnknownHandler(handler)})
	}
}
