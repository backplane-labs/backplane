package config_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/hashicorp/consul/api"

	"github.com/backplane-labs/backplane/internal/config"
)

func kvPairs(kv map[string]string, index uint64) api.KVPairs {
	out := make(api.KVPairs, 0, len(kv))
	for k, v := range kv {
		out = append(out, &api.KVPair{Key: k, Value: []byte(v), ModifyIndex: index})
	}

	return out
}

func TestDrift(t *testing.T) {
	t.Parallel()

	values := map[string]string{"a.b": "1", "c": "x"}
	synced := map[string]string{"config/svc/a/b": "1", "config/svc/c": "x", "config/svc/_revision": "3"}

	cases := map[string]struct {
		kv     map[string]string
		reason string
		keys   []string
	}{
		"in sync": {synced, "", nil},
		"empty":   {nil, config.ReasonMissing, []string{"config/svc/a/b", "config/svc/c"}},
		"stale": {
			map[string]string{"config/svc/a/b": "1", "config/svc/c": "x", "config/svc/_revision": "2"},
			config.ReasonStale, nil,
		},
		"no revision": {map[string]string{"config/svc/a/b": "1", "config/svc/c": "x"}, config.ReasonStale, nil},
		"edited": {
			map[string]string{"config/svc/a/b": "2", "config/svc/c": "x", "config/svc/_revision": "3"},
			config.ReasonEdited,
			[]string{"config/svc/a/b"},
		},
		"extra key": {
			map[string]string{"config/svc/a/b": "1", "config/svc/c": "x", "config/svc/d": "1", "config/svc/_revision": "3"},
			config.ReasonEdited,
			[]string{"config/svc/d"},
		},
	}

	for name, c := range cases {
		reason, keys := config.Drift("svc", 3, values, kvPairs(c.kv, 5))
		if reason != c.reason || (c.keys != nil && !slices.Equal(keys, c.keys)) {
			t.Errorf("%s: %q %v, want %q %v", name, reason, keys, c.reason, c.keys)
		}
	}

	// Folder markers of the Consul UI are not values; other services are
	// not this one's.
	all := append(kvPairs(synced, 5), &api.KVPair{Key: "config/svc/"}, &api.KVPair{Key: "config/svc/a/"},
		&api.KVPair{Key: "config/svc2/x", Value: []byte("1")})

	if reason, keys := config.Drift("svc", 3, values, all); reason != "" {
		t.Fatalf("folders and other services: %q %v", reason, keys)
	}
}

// Changed values and deletions, then _revision last, behind a CAS on the
// _revision that was read.
func TestTxns(t *testing.T) {
	t.Parallel()

	values := map[string]string{"a": "1", "b": "2"}

	txns := config.Txns("svc", 4, values,
		kvPairs(map[string]string{"config/svc/a": "1", "config/svc/old": "x", "config/svc/_revision": "3"}, 9))
	if len(txns) != 1 {
		t.Fatalf("one transaction: %d", len(txns))
	}

	got := make([]string, 0, len(txns[0]))
	for _, op := range txns[0] {
		got = append(got, fmt.Sprintf("%s %s %s %d", op.KV.Verb, op.KV.Key, op.KV.Value, op.KV.Index))
	}

	want := []string{
		"check-index config/svc/_revision  9",
		"set config/svc/b 2 0",
		"delete config/svc/old  0",
		"set config/svc/_revision 4 0",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ops:\n%v\nwant\n%v", got, want)
	}

	// Nothing there: the check is that _revision does not exist.
	if op := config.Txns("svc", 4, values, nil)[0][0]; op.KV.Verb != api.KVCheckNotExists {
		t.Fatalf("first op on empty kv: %s", op.KV.Verb)
	}
}

// More than 62 changes: transactions of 64, the last one with the CAS and
// _revision.
func TestTxnsChunked(t *testing.T) {
	t.Parallel()

	values := func(n int) map[string]string {
		out := map[string]string{}
		for i := range n {
			out[fmt.Sprintf("k%03d", i)] = "v"
		}

		return out
	}

	txns := config.Txns("svc", 1, values(100), nil)
	if len(txns) != 2 || len(txns[0]) != 38 || len(txns[1]) != config.MaxTxnOps {
		t.Fatalf("chunks: %d (%d, %d)", len(txns), len(txns[0]), len(txns[len(txns)-1]))
	}

	last := txns[1]
	if last[0].KV.Verb != api.KVCheckNotExists || last[len(last)-1].KV.Key != "config/svc/_revision" {
		t.Fatal("the last transaction checks first and writes _revision last")
	}

	sets := 0

	for _, ops := range txns {
		for _, op := range ops {
			if op.KV.Verb == api.KVSet && op.KV.Key != "config/svc/_revision" {
				sets++
			}
		}
	}

	if sets != 100 {
		t.Fatalf("values written: %d", sets)
	}

	for i, ops := range config.Txns("svc", 1, values(300), nil) {
		if len(ops) > config.MaxTxnOps {
			t.Fatalf("transaction %d has %d ops", i, len(ops))
		}
	}
}
