package errtext_test

import (
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/errtext"
)

// sqlError is a driver error naming its SQLSTATE, as pgconn.PgError does.
type sqlError struct{ code, detail string }

func (e *sqlError) Error() string    { return "duplicate key: " + e.detail }
func (e *sqlError) SQLState() string { return e.code }

func TestRender(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "plain", err: errors.New("boom"), want: "boom"},
		{
			name: "url error keeps scheme and host",
			err:  &url.Error{Op: "Get", URL: "https://s3.example.com/bucket/key?X-Amz-Signature=secret", Err: errors.New("timeout")},
			want: `Get "https://s3.example.com": timeout`,
		},
		{
			name: "database error keeps its code",
			err:  fmt.Errorf("insert user: %w", &sqlError{code: "23505", detail: "Key (email)=(a@b.c) already exists"}),
			want: "insert user: SQLSTATE 23505",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := errtext.Render(tc.err); got != tc.want {
				t.Fatalf("Render = %q, want %q", got, tc.want)
			}
		})
	}
}

func BenchmarkRender(b *testing.B) {
	err := fmt.Errorf("insert user: %w", &sqlError{code: "23505", detail: "Key (email)=(a@b.c) already exists"})

	for b.Loop() {
		_ = errtext.Render(err)
	}
}
