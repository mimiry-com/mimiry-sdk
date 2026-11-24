package mimiry

// Connecting to a session through the Mimiry gateway.
//
// Nothing connects to a session's machine directly. The machine connects out
// to the gateway's rendezvous points and keeps a connection open there; a
// client asks the platform for a ticket, opens a connection to a rendezvous
// point with it, and the rendezvous point pairs the two. The bytes it carries
// are the client's SSH connection to the session, end to end: the gateway
// never sees inside it, and SSH still decides who gets in.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// GatewayTicket opens connections to one session through the gateway.
type GatewayTicket struct {
	// Ticket is sent as "Authorization: Bearer <ticket>". Opaque.
	Ticket string `json:"ticket"`
	// ExpiresAt is when the ticket stops opening new connections. Open
	// connections are not affected.
	ExpiresAt time.Time `json:"expires_at"`
	// Rendezvous is the gateway's addresses (wss:// URLs), in the order to
	// try them.
	Rendezvous []string `json:"rendezvous"`
}

// GatewayTicket asks for a ticket to connect to a running session. A ticket
// lasts about a minute: get one for each connection. Most callers want
// Connect, which does this for them.
func (s *SessionsService) GatewayTicket(ctx context.Context, id string) (*GatewayTicket, error) {
	var out GatewayTicket
	if err := s.c.Do(ctx, http.MethodPost, computeAPI+"/sessions/"+url.PathEscape(id)+"/gateway-ticket", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// readyWait is how long a rendezvous point has to pair the connection with
// the session's machine. A variable for the tests.
var readyWait = 15 * time.Second

const (
	// gatewayPing keeps an idle connection open. The rendezvous point
	// answers it itself; it never reaches the session.
	gatewayPing = 30 * time.Second
	// finWait is how long Close waits for the session's side to finish.
	finWait = 10 * time.Second
	// maxGatewayFrame is the largest frame accepted (the relay's limit).
	maxGatewayFrame = 32 << 20
	// gatewayFrame is the largest frame sent.
	gatewayFrame = 1 << 20
)

// ErrSessionNotConnected is returned by Connect when no rendezvous point
// could reach the session's machine: it has not connected to the gateway yet,
// or has lost its connection. Retrying shortly may succeed.
var ErrSessionNotConnected = errors.New("mimiry: the session's machine is not connected to the gateway")

// Connect opens a connection to a running session's SSH server through the
// gateway. Speak SSH over it, and check that the server presents the
// session's SSH host key (Session.SSH.HostKey): the gateway carries the
// connection, it does not vouch for the other end.
//
// The connection lives until it is closed or ctx is cancelled. CloseWrite
// ends the sending half and keeps receiving, like TCP.
func (s *SessionsService) Connect(ctx context.Context, id string) (*GatewayConn, error) {
	t, err := s.GatewayTicket(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(t.Rendezvous) == 0 {
		return nil, errors.New("mimiry: the platform named no gateway address for this session")
	}
	var errs []error
	notConnected := true
	for _, base := range t.Rendezvous {
		gc, err := s.dialRendezvous(ctx, base, id, t.Ticket)
		if err == nil {
			return gc, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !errors.Is(err, ErrSessionNotConnected) {
			notConnected = false
		}
		errs = append(errs, fmt.Errorf("%s: %w", base, err))
	}
	if notConnected {
		return nil, fmt.Errorf("%w (%s)", ErrSessionNotConnected, strings.Join(t.Rendezvous, ", "))
	}
	return nil, fmt.Errorf("mimiry: could not connect through the gateway: %w", errors.Join(errs...))
}

func (s *SessionsService) dialRendezvous(ctx context.Context, base, id, ticket string) (*GatewayConn, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("bad gateway address: %w", err)
	}
	// The ticket travels in a header: never in the clear, except to this
	// machine (tests).
	if u.Scheme != "wss" && !(u.Scheme == "ws" && isLoopback(u.Hostname())) {
		return nil, fmt.Errorf("refusing gateway address %q: not wss://", base)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/s/" + url.PathEscape(id) + "/client"

	// Not the client's own http.Client: its overall timeout would end every
	// connection after a minute.
	hc := &http.Client{Transport: s.c.http.Transport}
	c, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPClient: hc,
		HTTPHeader: http.Header{
			"Authorization": {"Bearer " + ticket},
			"User-Agent":    {s.c.userAgent},
		},
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusServiceUnavailable {
			return nil, ErrSessionNotConnected
		}
		if resp != nil {
			return nil, fmt.Errorf("refused: HTTP %d", resp.StatusCode)
		}
		return nil, err
	}
	c.SetReadLimit(maxGatewayFrame)

	// Wait for "ready": the session's machine has joined. A deadline on the
	// read context would close the connection when it passed, so a timer
	// closes it instead, and only if ready never came.
	timer := time.AfterFunc(readyWait, func() { c.CloseNow() }) //nolint:errcheck // timeout
	for {
		typ, msg, err := c.Read(ctx)
		if err != nil {
			stopped := timer.Stop()
			c.CloseNow() //nolint:errcheck // already failed
			if !stopped {
				return nil, ErrSessionNotConnected
			}
			return nil, err
		}
		if typ == websocket.MessageText && string(msg) == "ready" {
			break
		}
		if typ == websocket.MessageBinary {
			timer.Stop()
			c.CloseNow() //nolint:errcheck // protocol error
			return nil, errors.New("gateway sent data before the session joined")
		}
	}
	if !timer.Stop() {
		c.CloseNow() //nolint:errcheck // the timer won the race
		return nil, ErrSessionNotConnected
	}
	return newGatewayConn(ctx, c, u.Host), nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// GatewayConn is a connection to a session through the gateway. It is a
// net.Conn with TCP's half-close: CloseWrite says this side has finished
// sending, and the session's side can still send.
//
// The end of each direction is explicit: a side that has finished sends how
// many bytes it sent, and the receiver checks the count, so a stream cut
// short is an error, never a normal end.
type GatewayConn struct {
	ctx  context.Context
	c    *websocket.Conn
	peer string

	rmu   sync.Mutex // Close drains reads itself, possibly beside a caller's Read
	buf   []byte
	recvd int64
	rerr  error // io.EOF once the session's side has finished and its count matched

	wmu     sync.Mutex
	sent    int64
	finSent bool

	stopPing  chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func newGatewayConn(ctx context.Context, c *websocket.Conn, peer string) *GatewayConn {
	g := &GatewayConn{ctx: ctx, c: c, peer: peer, stopPing: make(chan struct{})}
	go g.ping()
	return g
}

// ping keeps an idle connection open. The rendezvous point answers "pong"
// itself, and Read ignores it.
func (g *GatewayConn) ping() {
	t := time.NewTicker(gatewayPing)
	defer t.Stop()
	for {
		select {
		case <-g.stopPing:
			return
		case <-g.ctx.Done():
			return
		case <-t.C:
			g.wmu.Lock()
			err := g.c.Write(g.ctx, websocket.MessageText, []byte("ping"))
			g.wmu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// Read reads data from the session. It returns io.EOF when the session's
// side has finished sending and every byte it sent has arrived.
func (g *GatewayConn) Read(p []byte) (int, error) {
	g.rmu.Lock()
	defer g.rmu.Unlock()
	for len(g.buf) == 0 {
		if g.rerr != nil {
			return 0, g.rerr
		}
		typ, msg, err := g.c.Read(g.ctx)
		if err != nil {
			g.rerr = err
			return 0, err
		}
		if typ == websocket.MessageBinary {
			g.buf = msg
			continue
		}
		var n int64
		if _, err := fmt.Sscanf(string(msg), "fin %d", &n); err != nil {
			continue // "pong", or control text this version does not know
		}
		if n != g.recvd {
			g.rerr = fmt.Errorf("mimiry: gateway stream cut short: received %d of %d bytes", g.recvd, n)
		} else {
			g.rerr = io.EOF
		}
	}
	k := copy(p, g.buf)
	g.buf = g.buf[k:]
	g.recvd += int64(k)
	return k, nil
}

// Write sends data to the session.
func (g *GatewayConn) Write(p []byte) (int, error) {
	g.wmu.Lock()
	defer g.wmu.Unlock()
	if g.finSent {
		return 0, errors.New("mimiry: write after CloseWrite")
	}
	written := 0
	for written < len(p) {
		end := min(written+gatewayFrame, len(p))
		if err := g.c.Write(g.ctx, websocket.MessageBinary, p[written:end]); err != nil {
			return written, err
		}
		g.sent += int64(end - written)
		written = end
	}
	return written, nil
}

// CloseWrite ends the sending half. Reading continues until the session's
// side finishes too.
func (g *GatewayConn) CloseWrite() error {
	g.wmu.Lock()
	defer g.wmu.Unlock()
	if g.finSent {
		return nil
	}
	g.finSent = true
	return g.c.Write(g.ctx, websocket.MessageText, fmt.Appendf(nil, "fin %d", g.sent))
}

// Close ends the sending half, reads what the session's side still sends
// (for at most ten seconds), then closes. A stream that was cut short is
// reported.
func (g *GatewayConn) Close() error {
	g.closeOnce.Do(func() {
		close(g.stopPing)
		werr := g.CloseWrite()
		t := time.AfterFunc(finWait, func() { g.c.CloseNow() }) //nolint:errcheck // timeout
		defer t.Stop()
		_, rerr := io.Copy(io.Discard, g)
		g.c.Close(websocket.StatusNormalClosure, "") //nolint:errcheck // best effort after both ends finished
		g.closeErr = errors.Join(werr, rerr)
	})
	return g.closeErr
}

type gatewayAddr string

func (a gatewayAddr) Network() string { return "mimiry-gateway" }
func (a gatewayAddr) String() string  { return string(a) }

// LocalAddr names this side.
func (g *GatewayConn) LocalAddr() net.Addr { return gatewayAddr("client") }

// RemoteAddr is the rendezvous point the connection runs through.
func (g *GatewayConn) RemoteAddr() net.Addr { return gatewayAddr(g.peer) }

// SetDeadline is not supported: the connection ends by Close or by its
// context. It returns an error so a caller relying on deadlines finds out.
func (g *GatewayConn) SetDeadline(time.Time) error { return errDeadline }

// SetReadDeadline is not supported; see SetDeadline.
func (g *GatewayConn) SetReadDeadline(time.Time) error { return errDeadline }

// SetWriteDeadline is not supported; see SetDeadline.
func (g *GatewayConn) SetWriteDeadline(time.Time) error { return errDeadline }

var errDeadline = errors.New("mimiry: deadlines are not supported on a gateway connection; cancel its context")
