package console

import (
	"testing"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/registry"
)

func BenchmarkRPCPeers(b *testing.B) {
	const apiName = "example.console.v1.AdminService"

	svc := registry.Service{Name: "example", Manifests: make(map[string]*backplanev1.Manifest)}
	for _, version := range []string{"old", "new"} {
		svc.Manifests[version] = &backplanev1.Manifest{InternalServices: []string{apiName}, Ui: &backplanev1.UI{Hash: version}}
		svc.Instances = append(svc.Instances, registry.Instance{
			Registered: true, Healthy: true,
			State: &backplanev1.InstanceState{Version: version, Phase: backplanev1.InstancePhase_INSTANCE_PHASE_SERVING, Address: "127.0.0.1", PlatformPort: 9400},
		})
	}

	for _, tc := range []struct {
		name, hint string
		want       int
	}{{"unscoped", "", 2}, {"match", "example/old", 1}, {"fallback", "example/gone", 2}} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if len(rpcPeers(svc, apiName, tc.hint)) != tc.want {
					b.Fatal("unexpected candidates")
				}
			}
		})
	}
}
