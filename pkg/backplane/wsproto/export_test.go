package wsproto

import (
	"github.com/gopherex/ws-proto/wsrpc"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

// Middleware is the chain Serve installs, logging nowhere.
func Middleware() []wsrpc.Middleware { return middleware(testlog.Discard()) }
