package logsql_test

import (
	"testing"
	"time"

	"github.com/backplane-labs/backplane/internal/obs/logsql"
)

// Values are quoted and patterns escaped: nothing a value holds changes the
// query's structure.
func TestQueries(t *testing.T) {
	t.Parallel()

	service, msg := logsql.Field("service.name"), logsql.Field("exception.message")
	noon := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	for name, c := range map[string]struct {
		got, want string
	}{
		"eq":       {service.Eq(`a" OR *`).String(), `"service.name":="a\" OR *"`},
		"stream":   {logsql.Field("_stream_id").Eq("x").String(), `"_stream_id":"x"`},
		"contains": {msg.Contains("a.b(c)", true).String(), `"exception.message":~"(?i)a\\.b\\(c\\)"`},
		"prefix":   {msg.Prefix("x*").String(), `"exception.message":~"^x\\*"`},
		"set":      {service.Set().String(), `"service.name":*`},
		"and one":  {logsql.And(service.Eq("a")).String(), `"service.name":="a"`},
		"and":      {logsql.And(service.Eq("a"), logsql.Filter{}, logsql.Not(msg.Set())).String(), `("service.name":="a") AND (NOT ("exception.message":*))`},
		"or":       {logsql.Or(service.Eq("a"), service.Eq("b")).String(), `("service.name":="a") OR ("service.name":="b")`},
		"range":    {logsql.Range(noon, noon.Add(time.Minute)).String(), `_time:["2026-09-30T12:00:00Z","2026-09-30T12:01:00Z")`},
		"pipes": {
			logsql.From(service.Eq("a")).Fields(service, msg).Sort(10, logsql.Order{Field: "_time", Desc: true}).String(),
			`"service.name":="a" | fields "service.name", "exception.message" | sort by ("_time" desc) limit 10`,
		},
		"sum": {logsql.From(logsql.All()).Sum("a", "b", "c").String(), `* | math a + b as c`},
	} {
		if c.got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", name, c.got, c.want)
		}
	}
}

func TestFieldNamesAreChecked(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("a field name with quotes must panic")
		}
	}()

	_ = logsql.Field(`a" OR "b`).Eq("x")
}
