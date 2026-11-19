package valkey

import (
	"testing"
	"time"
)

// The section maps to valkey-go's options with explicit values: a zero
// field of a hand-built Config reads the default, pipeline_multiplex 0 is
// one connection (never valkey-go's CPU-derived count).
func TestClientOption(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		want func(t *testing.T, got Config)
	}{
		{name: "defaults", cfg: Config{Addr: "v:6379"}},
		{name: "explicit", cfg: Config{
			Addr: "v:6379", DialTimeout: time.Second, ConnWriteTimeout: 3 * time.Second, BlockingPoolSize: 8,
			BlockingPoolMinSize: 2, BlockingPoolCleanup: time.Minute, PipelineMultiplex: 1, BlockingPipeline: 100,
			MaxFlushDelay: 20 * time.Microsecond, DisableAutoPipelining: true, ReadBufferEachConn: 4096,
			WriteBufferEachConn: 8192, ConnLifetime: time.Hour,
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			o := tc.cfg.clientOption()
			want := tc.cfg

			if want.DialTimeout == 0 {
				want = Config{
					DialTimeout: DefaultDialTimeout, ConnWriteTimeout: DefaultConnWriteTimeout,
					BlockingPoolSize: DefaultBlockingPoolSize, BlockingPipeline: DefaultBlockingPipeline,
					ReadBufferEachConn: DefaultBufferEachConn, WriteBufferEachConn: DefaultBufferEachConn,
				}
			}

			if o.Dialer.Timeout != want.DialTimeout || o.ConnWriteTimeout != want.ConnWriteTimeout ||
				int64(o.BlockingPoolSize) != want.BlockingPoolSize || int64(o.BlockingPoolMinSize) != want.BlockingPoolMinSize ||
				o.BlockingPoolCleanup != want.BlockingPoolCleanup || int64(o.BlockingPipeline) != want.BlockingPipeline ||
				o.MaxFlushDelay != want.MaxFlushDelay || o.DisableAutoPipelining != want.DisableAutoPipelining ||
				int64(o.ReadBufferEachConn) != want.ReadBufferEachConn || int64(o.WriteBufferEachConn) != want.WriteBufferEachConn ||
				o.ConnLifetime != want.ConnLifetime {
				t.Fatalf("option = %+v", o)
			}

			if wantMux := map[int64]int{0: -1, 1: 1}[want.PipelineMultiplex]; o.PipelineMultiplex != wantMux {
				t.Fatalf("pipeline multiplex %d, want %d", o.PipelineMultiplex, wantMux)
			}
		})
	}
}

// Sentinels are the initial addresses and the master set is followed; the
// data nodes keep the section's credentials, the sentinels their own.
func TestClientOptionSentinel(t *testing.T) {
	t.Parallel()

	o := Config{
		Username: "app", Password: "dp",
		Sentinel: Sentinel{MasterSet: "cache", Addrs: "s1:26379, s2:26379", Username: "sentinel", Password: "sp"},
	}.clientOption()

	if len(o.InitAddress) != 2 || o.InitAddress[1] != "s2:26379" || o.Sentinel.MasterSet != "cache" ||
		o.Sentinel.Username != "sentinel" || o.Sentinel.Password != "sp" || o.Username != "app" || o.Password != "dp" {
		t.Fatalf("options %+v / %+v", o.InitAddress, o.Sentinel)
	}
}
