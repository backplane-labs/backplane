package config

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInboxPrefix: an inbox prefix NATS would refuse.
var ErrInboxPrefix = errors.New("inbox_prefix is a subject prefix without wildcards or a trailing dot")

// CheckInboxPrefix: empty (NATS's _INBOX), or subject tokens without
// wildcards, whitespace or a trailing dot.
func CheckInboxPrefix(p string) error {
	if p == "" {
		return nil
	}

	if strings.ContainsAny(p, "*> \t\r\n") || strings.HasPrefix(p, ".") || strings.HasSuffix(p, ".") ||
		strings.Contains(p, "..") {
		return fmt.Errorf("%w: %q", ErrInboxPrefix, p)
	}

	return nil
}
