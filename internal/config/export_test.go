package config

import (
	"encoding/json"

	"github.com/hashicorp/consul/api"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/registry"
)

// Reasons KV is rewritten, and Consul's transaction limit.
const (
	ReasonMissing = reasonMissing
	ReasonStale   = reasonStale
	ReasonEdited  = reasonEdited
	MaxTxnOps     = maxTxnOps
)

// Prefix is config/<service>/.
func Prefix(service string) string { return prefix(service) }

// Parse checks and encodes an override against a configuration section:
// the values as saved and as delivered to KV.
func Parse(sec *backplanev1.ConfigSection, in map[string]string) (map[string]json.RawMessage, map[string]string, []Violation) {
	o, v := parse(sec, in)

	return o.values, o.kv, v
}

// Check parses and validates in against svc, as Manager.Validate does.
func Check(svc registry.Service, in map[string]string) ([]Violation, error) {
	var e engines

	_, violations, err := e.check(svc, in)

	return violations, err
}

// Drift is why the service's pairs differ from its revision's values.
func Drift(service string, revision int64, kv map[string]string, pairs api.KVPairs) (string, []string) {
	return target{service: service, revision: revision, kv: kv}.drift(pairsOf(service, pairs))
}

// Txns are the transactions that take pairs to the revision's values.
func Txns(service string, revision int64, kv map[string]string, pairs api.KVPairs) []api.TxnOps {
	return target{service: service, revision: revision, kv: kv}.txns(pairsOf(service, pairs))
}
