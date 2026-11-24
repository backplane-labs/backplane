// Package formatter demonstrates a standalone HTTP handler with live configuration.
package formatter

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

type Config struct {
	Prefix config.Live[string] `json:"prefix" schemapb:"default=Welcome"`
	Suffix config.Live[string] `json:"suffix" schemapb:"default=!"`
}

// HTTP formats a name with the current prefix and suffix.
func HTTP(cfg *Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fmt.Sprintf("%s, %s%s", cfg.Prefix.Get(), name, cfg.Suffix.Get()))
	})
}
