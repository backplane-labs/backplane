// Package temporaltest locates a real Temporal test server.
package temporaltest

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
)

const (
	// Namespace of the dev server.
	Namespace = "default"
	nameBytes = 5
)

// Addr is the dev server's address; the test is skipped without one.
func Addr(tb testing.TB) string {
	tb.Helper()

	addr := os.Getenv("BACKPLANE_TEST_TEMPORAL")
	if addr == "" {
		tb.Skip("BACKPLANE_TEST_TEMPORAL not set (make up)")
	}

	return addr
}

// Name is a unique service name: [a-z][a-z0-9-]*.
func Name(prefix string) string {
	b := make([]byte, nameBytes)
	_, _ = rand.Read(b)

	return prefix + "-" + hex.EncodeToString(b)
}
