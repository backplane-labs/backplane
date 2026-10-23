package listener

import (
	"bufio"
	"crypto/tls"
	"net"
)

// handshakeRecord is the first byte of a TLS record carrying a handshake:
// what a client's first flight starts with. Plain HTTP and gRPC start with
// an ASCII letter.
const handshakeRecord = 0x16

// sniffing hands out each accepted connection as TLS when its first byte
// starts a handshake, as it is otherwise. The byte is read lazily, on the
// connection's first read (under cmux's read timeout), so Accept never
// blocks on a silent client.
type sniffing struct {
	net.Listener

	tls *tls.Config
}

func (s *sniffing) Accept() (net.Conn, error) {
	conn, err := s.Listener.Accept()
	if err != nil {
		return nil, err //nolint:wrapcheck // the listener's own error, for cmux
	}

	return &sniffed{Conn: conn, cfg: s.tls}, nil
}

// sniffed decides on its first read whether it is TLS.
type sniffed struct {
	net.Conn

	cfg     *tls.Config
	decided net.Conn // the connection reads and writes go to once decided
}

func (c *sniffed) Read(p []byte) (int, error) {
	if c.decided == nil {
		c.decide()
	}

	return c.decided.Read(p) //nolint:wrapcheck // a connection's own error
}

func (c *sniffed) Write(p []byte) (int, error) {
	if c.decided == nil {
		c.decide()
	}

	return c.decided.Write(p) //nolint:wrapcheck // a connection's own error
}

// decide peeks the first byte: a handshake makes the connection TLS.
func (c *sniffed) decide() {
	r := bufio.NewReaderSize(c.Conn, 1)
	plain := &buffered{Conn: c.Conn, r: r}

	first, err := r.Peek(1)
	if err == nil && first[0] == handshakeRecord {
		c.decided = tls.Server(plain, c.cfg)

		return
	}

	c.decided = plain
}

// TLS is the TLS connection when the client started one; nil otherwise.
func (c *sniffed) TLS() *tls.Conn {
	t, _ := c.decided.(*tls.Conn)

	return t
}

// buffered reads through the reader that peeked.
type buffered struct {
	net.Conn

	r *bufio.Reader
}

func (b *buffered) Read(p []byte) (int, error) { return b.r.Read(p) } //nolint:wrapcheck // the conn's own
