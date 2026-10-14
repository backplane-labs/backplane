package exception_test

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/exception"
)

func values(fields []xlog.Field) map[string]string {
	out := map[string]string{}
	for _, f := range fields {
		out[f.Key] = f.StringValue()
	}

	return out
}

// An error is typed by its deepest telling cause and carries the caller's stack;
// a panic says it was one.
func TestFields(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("load config: %w", &fs.PathError{Op: "open", Path: "/etc/x", Err: fs.ErrNotExist})
	got := values(exception.Fields(err))

	if got[exception.Type] != "*fs.PathError" {
		t.Fatalf("type %q", got[exception.Type])
	}

	if got[exception.Message] != err.Error() || got[exception.EventName] != "exception" ||
		!strings.Contains(got[exception.Stacktrace], "TestFields") {
		t.Fatalf("fields %v", got)
	}

	if kind := values(exception.Fields(fmt.Errorf("x: %w", errors.New("y"))))[exception.Type]; kind != "error" {
		t.Fatalf("sentinel type %q", kind)
	}

	if kind := values(exception.Panic("boom", []byte("stack")))[exception.Type]; kind != "panic" {
		t.Fatalf("panic type %q", kind)
	}

	if kind := values(exception.Panic(errors.New("x"), nil))[exception.Type]; kind != "panic: *errors.errorString" {
		t.Fatalf("panic error type %q", kind)
	}
}
