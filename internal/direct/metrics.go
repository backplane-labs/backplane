package direct

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics of the direct runtime (on its node's meter):
//
//	backplane.direct.runs{kind, outcome}   ended runs: ok | failed | canceled
//	backplane.direct.run.duration{kind, outcome}   s, a run start to end
//	backplane.direct.records_dropped   run records lost to a full queue or a database that is down
type metrics struct {
	runs    metric.Int64Counter
	runTime metric.Float64Histogram
	drops   metric.Int64Counter
}

func newMetrics(m metric.Meter) metrics {
	if m == nil {
		return metrics{}
	}

	runs, _ := m.Int64Counter("backplane.direct.runs", metric.WithDescription("direct runs ended, by outcome"))
	runTime, _ := m.Float64Histogram("backplane.direct.run.duration", metric.WithUnit("s"),
		metric.WithDescription("direct runs, start to end"))
	drops, _ := m.Int64Counter("backplane.direct.records_dropped",
		metric.WithDescription("direct run records lost: the write queue was full or the database down"))

	return metrics{runs: runs, runTime: runTime, drops: drops}
}

func (m metrics) run(ctx context.Context, kind Kind, err error, took time.Duration) {
	if m.runs == nil {
		return
	}

	outcome := "ok"

	switch {
	case errors.Is(err, context.Canceled):
		outcome = "canceled"
	case err != nil:
		outcome = "failed"
	}

	attrs := metric.WithAttributes(attribute.String("kind", string(kind)), attribute.String("outcome", outcome))
	m.runs.Add(ctx, 1, attrs)
	m.runTime.Record(ctx, took.Seconds(), attrs)
}

func (m metrics) dropped() {
	if m.drops != nil {
		m.drops.Add(context.Background(), 1)
	}
}
