// Package natstest runs real NATS servers with JetStream inside the test's
// process, for tests of what uses NATS: no container, random ports, the
// stores in the test's temporary directory. A Cluster loses and regains
// nodes the way a JetStream cluster loses its meta leader: the surviving
// node keeps its clients connected while JetStream does not answer.
package natstest

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

const (
	// readyWait bounds a server's start.
	readyWait = 10 * time.Second
	// loopback is where every server listens.
	loopback = "127.0.0.1"
)

// Start runs a single server with JetStream; it shuts down when the test
// ends. Its client URL is ClientURL().
func Start(tb testing.TB) *server.Server {
	tb.Helper()

	return run(tb, &server.Options{
		Host: loopback, Port: server.RANDOM_PORT, NoLog: true, NoSigs: true,
		JetStream: true, StoreDir: tb.TempDir(),
	})
}

// Cluster is a JetStream cluster of servers in the test's process.
type Cluster struct {
	opts    []*server.Options
	servers []*server.Server
}

// StartCluster runs n clustered servers with JetStream; they shut down when
// the test ends.
func StartCluster(tb testing.TB, n int) *Cluster {
	tb.Helper()

	clients, routes := freePorts(tb, n), freePorts(tb, n)

	urls := make([]string, n)
	for i := range n {
		urls[i] = "nats://" + loopback + ":" + strconv.Itoa(routes[i])
	}

	c := &Cluster{opts: make([]*server.Options, n), servers: make([]*server.Server, n)}

	for i := range n {
		c.opts[i] = &server.Options{
			ServerName: "n" + strconv.Itoa(i), Host: loopback, Port: clients[i], NoLog: true, NoSigs: true,
			JetStream: true, StoreDir: tb.TempDir(),
			Cluster: server.ClusterOpts{Name: "natstest", Host: loopback, Port: routes[i]},
			Routes:  server.RoutesFromStr(joinOthers(urls, i)),
		}
	}

	for i := range n {
		c.Restart(tb, i)
	}

	return c
}

// URL is node i's client URL.
func (c *Cluster) URL(i int) string {
	return "nats://" + loopback + ":" + strconv.Itoa(c.opts[i].Port)
}

// Stop shuts node i down.
func (c *Cluster) Stop(i int) {
	c.servers[i].Shutdown()
	c.servers[i].WaitForShutdown()
}

// Restart starts node i again on its ports with its store.
func (c *Cluster) Restart(tb testing.TB, i int) {
	tb.Helper()

	opts := *c.opts[i]
	c.servers[i] = run(tb, &opts)
}

func run(tb testing.TB, opts *server.Options) *server.Server {
	tb.Helper()

	srv, err := server.NewServer(opts)
	if err != nil {
		tb.Fatalf("natstest: %v", err)
	}

	srv.Start()
	tb.Cleanup(srv.Shutdown)

	if !srv.ReadyForConnections(readyWait) {
		tb.Fatal("natstest: the server did not start")
	}

	return srv
}

// freePorts reserves n free TCP ports on the loopback.
func freePorts(tb testing.TB, n int) []int {
	tb.Helper()

	ports := make([]int, n)

	for i := range n {
		ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", loopback+":0")
		if err != nil {
			tb.Fatalf("natstest: %v", err)
		}

		addr, err := netip.ParseAddrPort(ln.Addr().String())
		if err != nil {
			tb.Fatalf("natstest: %v", err)
		}

		ports[i] = int(addr.Port())

		if err := ln.Close(); err != nil {
			tb.Fatalf("natstest: %v", err)
		}
	}

	return ports
}

// joinOthers is the route list of node self: every other node.
func joinOthers(urls []string, self int) string {
	return strings.Join(slices.Delete(slices.Clone(urls), self, self+1), ",")
}
