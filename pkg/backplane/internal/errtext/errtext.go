// Package errtext renders errors for telemetry: what an error's text may
// carry that must not leave the service is cut, the error itself is left
// as it is (errors.Is/As keep working). A *url.Error loses its URL's path
// and query (signed links, tokens); a PostgreSQL error its message, detail
// and context (row values). The SDK's root logger and tracers render every
// error through it.
package errtext

import (
	"github.com/gopherex/xlog"
	xlogpgx "github.com/gopherex/xlog/contrib/libs/pgx"
)

// Render is err's text for logs and spans.
func Render(err error) string {
	return xlog.NewErrorRenderer(xlog.RedactURL, xlogpgx.RedactPgError)(err)
}
