package api

// Origins is the browser origin policy of a WebSocket endpoint (ws-proto).
// It is a required argument where it applies; the
// zero value allows only same-origin browsers and non-browser clients.
type Origins struct {
	any      bool
	patterns []string
}

// AllowOrigins accepts upgrades from browsers on these host patterns
// ("app.example.com", "*.example.com") in addition to same-origin ones.
func AllowOrigins(patterns ...string) Origins { return Origins{patterns: patterns} }

// AnyOrigin accepts every origin: for endpoints that authenticate each call
// themselves and never rely on cookies.
func AnyOrigin() Origins { return Origins{any: true} }

// IsAny reports whether every origin is accepted.
func (o Origins) IsAny() bool { return o.any }

// Patterns are the accepted host patterns.
func (o Origins) Patterns() []string { return append([]string(nil), o.patterns...) }
