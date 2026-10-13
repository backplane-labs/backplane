package routes

import (
	"net"
	"net/http"
	"strings"
)

// Hosts serves one path prefix for several hosts, using HTTP Host matching
// them: an exact host, then the longest "*.suffix" wildcard, then the route
// without a host. A request no route matches gets 404.
type Hosts struct {
	exact map[string]http.Handler
	wild  map[string]http.Handler // key: ".suffix"
	any   http.Handler
}

// NewHosts creates an empty host switch.
func NewHosts() *Hosts {
	return &Hosts{exact: map[string]http.Handler{}, wild: map[string]http.Handler{}}
}

// Add serves h for host ("" = any host); false when host is taken.
func (s *Hosts) Add(host string, h http.Handler) bool {
	host = strings.ToLower(host)

	switch {
	case host == "":
		if s.any != nil {
			return false
		}

		s.any = h
	case strings.HasPrefix(host, "*."):
		suffix := host[1:]
		if _, dup := s.wild[suffix]; dup {
			return false
		}

		s.wild[suffix] = h
	default:
		if _, dup := s.exact[host]; dup {
			return false
		}

		s.exact[host] = h
	}

	return true
}

func (s *Hosts) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h := s.match(r.Host); h != nil {
		h.ServeHTTP(w, r)

		return
	}

	http.NotFound(w, r)
}

func (s *Hosts) match(host string) http.Handler {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	host = strings.ToLower(host)
	if h, ok := s.exact[host]; ok {
		return h
	}

	var (
		best    http.Handler
		longest int
	)

	for suffix, h := range s.wild {
		if strings.HasSuffix(host, suffix) && len(suffix) > longest {
			best, longest = h, len(suffix)
		}
	}

	if best != nil {
		return best
	}

	return s.any
}
