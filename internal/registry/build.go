package registry

import (
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/consul/api"
	"google.golang.org/protobuf/proto"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
)

// Prefix of everything the SDK writes to Consul KV (§4.2, §17).
const Prefix = "backplane/services/"

// Key kinds under Prefix/<service>/.
const (
	kindManifests = "manifests"
	kindInstances = "instances"
)

// consulService is Consul's own catalog entry: not a service of the
// installation.
const consulService = "consul"

// kvService is what KV holds for one service.
type kvService struct {
	manifests map[string]*backplanev1.Manifest      // by version
	states    map[string]*backplanev1.InstanceState // by instance id
}

// parsed is one decoded KV value, kept while its ModifyIndex holds: a
// change of one key does not decode the others again, and unchanged values
// keep their pointers.
type parsed struct {
	modify uint64
	msg    proto.Message
}

// badValue is a KV value that does not decode.
type badValue struct {
	modify uint64
	err    error
}

// parseKV decodes the pairs under Prefix. Keys of other shapes are
// ignored; values that do not decode are skipped and reported by key. The
// returned cache replaces cache.
func parseKV(
	pairs api.KVPairs, cache map[string]parsed,
) (map[string]*kvService, map[string]parsed, map[string]badValue) {
	out := map[string]*kvService{}
	next := make(map[string]parsed, len(pairs))
	bad := map[string]badValue{}

	for _, kv := range pairs {
		name, kind, id, ok := splitKey(kv.Key)
		if !ok {
			continue
		}

		msg, err := decode(kv, kind, cache)
		if err != nil {
			bad[kv.Key] = badValue{modify: kv.ModifyIndex, err: err}

			continue
		}

		next[kv.Key] = parsed{modify: kv.ModifyIndex, msg: msg}

		svc := out[name]
		if svc == nil {
			svc = &kvService{
				manifests: map[string]*backplanev1.Manifest{}, states: map[string]*backplanev1.InstanceState{},
			}
			out[name] = svc
		}

		switch m := msg.(type) {
		case *backplanev1.Manifest:
			svc.manifests[id] = m
		case *backplanev1.InstanceState:
			svc.states[id] = m
		}
	}

	return out, next, bad
}

// splitKey: backplane/services/<name>/<kind>/<id>.
func splitKey(key string) (string, string, string, bool) {
	rest, ok := strings.CutPrefix(key, Prefix)
	if !ok {
		return "", "", "", false
	}

	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" ||
		(parts[1] != kindManifests && parts[1] != kindInstances) {
		return "", "", "", false
	}

	return parts[0], parts[1], parts[2], true
}

func decode(kv *api.KVPair, kind string, cache map[string]parsed) (proto.Message, error) {
	if c, ok := cache[kv.Key]; ok && c.modify == kv.ModifyIndex {
		return c.msg, nil
	}

	var msg proto.Message = &backplanev1.Manifest{}
	if kind == kindInstances {
		msg = &backplanev1.InstanceState{}
	}

	if err := proto.Unmarshal(kv.Value, msg); err != nil {
		return nil, fmt.Errorf("%s: %w", kind, err)
	}

	return msg, nil
}

// serviceNames are the services the registry follows: every service with
// something in KV, and every catalog service but Consul's own.
func serviceNames(kv map[string]*kvService, catalog map[string][]string) []string {
	names := make([]string, 0, len(kv)+len(catalog))
	for name := range kv {
		names = append(names, name)
	}

	for name := range catalog {
		if _, ok := kv[name]; !ok && name != consulService {
			names = append(names, name)
		}
	}

	slices.Sort(names)

	return names
}

// build merges KV and health into the services of a snapshot. health holds
// the entries of the services whose health is loaded.
func build(names []string, kv map[string]*kvService, health map[string][]*api.ServiceEntry) map[string]Service {
	out := make(map[string]Service, len(names))

	for _, name := range names {
		svc := Service{Name: name, Manifests: map[string]*backplanev1.Manifest{}}

		byID := map[string]*Instance{}
		get := func(id string) *Instance {
			if in := byID[id]; in != nil {
				return in
			}

			in := &Instance{ID: id}
			byID[id] = in

			return in
		}

		if k := kv[name]; k != nil {
			svc.Manifests = k.manifests

			for id, st := range k.states {
				get(id).State = st
			}
		}

		for _, e := range health[name] {
			if e.Service == nil {
				continue
			}

			in := get(e.Service.ID)
			in.Registered = true
			in.Healthy = e.Checks.AggregatedStatus() == api.HealthPassing
			in.Address = e.Service.Address
			in.Port = uint32(max(0, e.Service.Port)) //nolint:gosec // a port fits

			if in.Address == "" && e.Node != nil {
				in.Address = e.Node.Address
			}

			if len(e.Service.Tags) > 0 {
				in.Tags = slices.Clone(e.Service.Tags)
			}
		}

		for _, in := range byID {
			svc.Instances = append(svc.Instances, *in)
		}

		slices.SortFunc(svc.Instances, func(a, b Instance) int { return strings.Compare(a.ID, b.ID) })

		out[name] = svc
	}

	return out
}
