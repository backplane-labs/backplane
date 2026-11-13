// Package errtext renders errors for telemetry: what an error's text may
// carry that must not leave the service is cut, the error itself is left
// as it is (errors.Is/As keep working). A *url.Error loses its URL's path
// and query (signed links, tokens); a database error (any error with a
// SQLState method, PostgreSQL's among them) keeps only its SQLSTATE code,
// its message, detail and context dropped (row values). The SDK's root
// logger and tracers render every error through it. It imports no database
// driver: the SDK's contracts stay free of optional backends.
package errtext

import (
	"reflect"

	"github.com/gopherex/xlog"
)

// Render is err's text for logs and spans.
func Render(err error) string {
	return xlog.NewErrorRenderer(xlog.RedactURL, redactSQLState)(err)
}

// redactSQLState renders a database error (pgconn.PgError, and any driver
// error naming its SQLSTATE) as its code alone. The renderer asks layer by
// layer, so only this layer's own type counts: a wrapper's text ("insert
// user: ") is kept and the database error under it rendered in turn.
func redactSQLState(err error) (string, bool) {
	m := reflect.ValueOf(err).MethodByName("SQLState")
	if !m.IsValid() || m.Type().NumIn() != 0 || m.Type().NumOut() != 1 || m.Type().Out(0).Kind() != reflect.String {
		return "", false
	}

	return "SQLSTATE " + m.Call(nil)[0].String(), true
}
