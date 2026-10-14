package backplane

import (
	"net"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// NewLogger is the service logger built for id.
func NewLogger(id Identity, given *xlog.Logger, cfg config.Backplane) *xlog.Logger {
	return id.logger(given, cfg)
}

// WarnUnreachable reports a loopback advertise address.
func WarnUnreachable(id Identity, log *xlog.Logger, cfg config.Backplane) {
	id.warnUnreachable(log, cfg)
}

// PickAddress is the address hostAddress prefers among ips.
func PickAddress(ips []net.IP) string { return pickAddress(ips) }
