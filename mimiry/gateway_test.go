package mimiry

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// rendezvous is a fake gateway rendezvous point that plays the session's side
// itself: it pairs at once (or answers as told), reads the client's stream to
// its fin, and sends the same bytes back followed by its own fin.
type rendezvous struct {
	t *testing.T
	// status, when set, refuses every connection with it.
	status int
	// silent never sends "ready".
	silent bool
	// shortFin claims one byte more than it sent.
	shortFin bool
	got      chan []byte // what the client sent, once its fin arrived
}

func newRendezvous(t *testing.T, r *rendezvous) string {
	t.Helper()
	r.t = t
	r.got = make(chan []byte, 1)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func (r *rendezvous) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/s/s-1/client" {
		r.t.Errorf("path %s", req.URL.Path)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tkt" {
		r.t.Errorf("Authorization %q", got)
	}
	if r.status != 0 {
		http.Error(w, "refused", r.status)
		return
	}
	c, err := websocket.Accept(w, req, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(maxGatewayFrame)
	ctx := req.Context()
	if r.silent {
		c.Read(ctx) //nolint:errcheck // until the client gives up
		return
	}
	c.Write(ctx, websocket.MessageText, []byte("ready")) //nolint:errcheck
	var in bytes.Buffer
	for {
		typ, msg, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			in.Write(msg)
			// Control text the client must ignore, in the middle of data.
			c.Write(ctx, websocket.MessageText, []byte("pong")) //nolint:errcheck
			continue
		}
		var n int
		if _, err := fmt.Sscanf(string(msg), "fin %d", &n); err == nil {
			if n != in.Len() {
				r.t.Errorf("client fin %d, received %d", n, in.Len())
			}
			break
		}
	}
	r.got <- in.Bytes()
	for b := in.Bytes(); len(b) > 0; {
		k := min(len(b), 64<<10)
		c.Write(ctx, websocket.MessageBinary, b[:k]) //nolint:errcheck
		b = b[k:]
	}
	n := in.Len()
	if r.shortFin {
		n++
	}
	c.Write(ctx, websocket.MessageText, fmt.Appendf(nil, "fin %d", n)) //nolint:errcheck
	c.Read(ctx)                                                        //nolint:errcheck // until the client closes
}

// gatewayClient is a client whose API issues a ticket naming points.
func gatewayClient(t *testing.T, points ...string) *Client {
	return computeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/compute/v1/sessions/s-1/gateway-ticket" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprintf(w, `{"ticket":"tkt","expires_at":"2026-10-28T09:01:00Z","rendezvous":["%s"]}`,
			strings.Join(points, `","`))
	})
}

func TestConnectCarriesDataBothWaysAndEndsWithMatchingCounts(t *testing.T) {
	down := newRendezvous(t, &rendezvous{status: http.StatusServiceUnavailable})
	up := &rendezvous{}
	c := gatewayClient(t, down, newRendezvous(t, up))

	gc, err := c.Sessions().Connect(context.Background(), "s-1")
	if err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 3<<20+17) // several frames, not a frame multiple
	rand.Read(want)
	go func() {
		if _, err := gc.Write(want); err != nil {
			t.Error(err)
		}
		if err := gc.CloseWrite(); err != nil {
			t.Error(err)
		}
	}()
	got, err := io.ReadAll(gc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("echo differs: %d bytes back of %d", len(got), len(want))
	}
	if sent := <-up.got; !bytes.Equal(sent, want) {
		t.Fatalf("the session received %d bytes of %d", len(sent), len(want))
	}
	if err := gc.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConnectReportsAStreamCutShort(t *testing.T) {
	c := gatewayClient(t, newRendezvous(t, &rendezvous{shortFin: true}))
	gc, err := c.Sessions().Connect(context.Background(), "s-1")
	if err != nil {
		t.Fatal(err)
	}
	gc.Write([]byte("hello")) //nolint:errcheck
	gc.CloseWrite()           //nolint:errcheck
	if _, err := io.ReadAll(gc); err == nil || !strings.Contains(err.Error(), "cut short") {
		t.Fatalf("err = %v, want a stream cut short", err)
	}
}

func TestConnectWhenNoRendezvousReachesTheSession(t *testing.T) {
	c := gatewayClient(t,
		newRendezvous(t, &rendezvous{status: http.StatusServiceUnavailable}),
		newRendezvous(t, &rendezvous{status: http.StatusServiceUnavailable}))
	if _, err := c.Sessions().Connect(context.Background(), "s-1"); !errors.Is(err, ErrSessionNotConnected) {
		t.Fatalf("err = %v, want ErrSessionNotConnected", err)
	}
}

func TestConnectGivesUpWhenThePairingNeverCompletes(t *testing.T) {
	defer func(d time.Duration) { readyWait = d }(readyWait)
	readyWait = 200 * time.Millisecond
	c := gatewayClient(t, newRendezvous(t, &rendezvous{silent: true}))
	if _, err := c.Sessions().Connect(context.Background(), "s-1"); !errors.Is(err, ErrSessionNotConnected) {
		t.Fatalf("err = %v, want ErrSessionNotConnected", err)
	}
}

func TestConnectNamesARefusal(t *testing.T) {
	c := gatewayClient(t, newRendezvous(t, &rendezvous{status: http.StatusUnauthorized}))
	_, err := c.Sessions().Connect(context.Background(), "s-1")
	if err == nil || errors.Is(err, ErrSessionNotConnected) || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("err = %v, want the 401 named", err)
	}
}

func TestConnectNeverSendsTheTicketInTheClear(t *testing.T) {
	c := gatewayClient(t, "ws://gateway.example.com", "http://127.0.0.1:1")
	_, err := c.Sessions().Connect(context.Background(), "s-1")
	if err == nil || strings.Count(err.Error(), "not wss://") != 2 {
		t.Fatalf("err = %v, want both addresses refused", err)
	}
}
