package backplane

import (
	"context"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/errtext"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/exception"
)

// RenderError is err's text as the SDK's root logger and tracers write it:
// a *url.Error without its URL's path and query, a database error (any
// error with a SQLState method) as its SQLSTATE code alone. Hand it to
// whatever else writes errors out of the service (xlog.WithErrorRenderer,
// xtrace.WithErrorRenderer, an outbox's last error) so that every place
// cuts the same.
func RenderError(err error) string { return errtext.Render(err) }

// CaptureError logs err as an OpenTelemetry exception record (its type,
// message and the caller's stack, and the context's trace), which the
// console's Errors section lists with its trace and related logs. It only
// logs: it neither stops nor retries anything, and returns nothing to check.
//
//	if err := charge(ctx); err != nil {
//		backplane.CaptureError(ctx, c.Log(), err, xlog.String("order", id))
//		return err
//	}
func CaptureError(ctx context.Context, log *xlog.Logger, err error, fields ...xlog.Field) {
	if err == nil || log == nil {
		return
	}

	log.Ctx().Error(ctx, err.Error(), append(exception.Fields(err), fields...)...)
}
