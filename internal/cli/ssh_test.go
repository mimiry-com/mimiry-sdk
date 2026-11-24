package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/mimiry-com/mimiry-sdk/mimiry"
)

func TestSplitSSHArgsStopsWhereSSHWould(t *testing.T) {
	cases := []struct {
		args          []string
		opts, command []string
	}{
		{nil, nil, nil},
		{[]string{"nvidia-smi", "-L"}, nil, []string{"nvidia-smi", "-L"}},
		{[]string{"-L", "8888:localhost:8888"}, []string{"-L", "8888:localhost:8888"}, nil},
		{[]string{"-vL", "1:h:2", "top"}, []string{"-vL", "1:h:2"}, []string{"top"}},
		{[]string{"-L1:h:2", "-A", "ls", "-la"}, []string{"-L1:h:2", "-A"}, []string{"ls", "-la"}},
		{[]string{"-o", "LogLevel=ERROR", "--", "-x"}, []string{"-o", "LogLevel=ERROR"}, []string{"-x"}},
	}
	for _, c := range cases {
		opts, command := splitSSHArgs(c.args)
		if !slices.Equal(opts, c.opts) || !slices.Equal(command, c.command) {
			t.Errorf("%q: options %q, command %q; want %q, %q", c.args, opts, command, c.opts, c.command)
		}
	}
}

func TestTheSignInKeyIsOfferedUnlessTheCallerNamesOne(t *testing.T) {
	base := sshInvocation{proxy: "P", alias: "mimiry-s-1", knownHosts: "/kh", user: "root", key: "/k"}
	got := sshCommandLine(base)
	want := []string{
		"-o", "ProxyCommand=P", "-o", "HostKeyAlias=mimiry-s-1", "-o", "UserKnownHostsFile=/kh",
		"-o", "StrictHostKeyChecking=yes", "-o", "UpdateHostKeys=no", "-o", "CheckHostIP=no",
		"-l", "root", "-i", "/k", "-o", "IdentitiesOnly=yes", "mimiry-s-1",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}

	own := base
	own.opts, own.command = []string{"-Ai", "/mine"}, []string{"uptime"}
	got = sshCommandLine(own)
	if slices.Contains(got, "/k") || slices.Contains(got, "IdentitiesOnly=yes") || !slices.Equal(got[len(got)-4:], []string{"-Ai", "/mine", "mimiry-s-1", "uptime"}) {
		t.Fatalf("got %q", got)
	}
	// "-i" as the value of another option is not -i.
	if hasOption([]string{"-o", "-i"}, 'i') {
		t.Fatal("an option's value was read as an option")
	}
}

func TestTheProxyCommandSurvivesTheShellAndSSHsTokens(t *testing.T) {
	got := proxyCommand("/opt/my tools/mimiry", "https://h.example", "/keys/it's 100%", "s-1")
	want := `'/opt/my tools/mimiry' '--host' 'https://h.example' '--key' '/keys/it'\''s 100%%' 'session' 'proxy' 's-1'`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := proxyCommand("/m", "https://h", "", "s-1"); strings.Contains(got, "--key") {
		t.Fatalf("a key nobody named was passed: %s", got)
	}
}

func TestSSHRefusesASessionItCannotReach(t *testing.T) {
	ready := func() *mimiry.Session {
		return &mimiry.Session{ID: "s-1", State: mimiry.SessionRunning, SSHEnabled: true,
			SSH: &mimiry.SSHInfo{Username: "root", HostKey: "ssh-ed25519 AAAA"}}
	}
	if err := sshReady(ready()); err != nil {
		t.Fatalf("a running session was refused: %v", err)
	}
	cases := []struct {
		change func(*mimiry.Session)
		want   string
	}{
		{func(s *mimiry.Session) { s.State = mimiry.SessionPullingImage }, "still starting (pulling_image)"},
		{func(s *mimiry.Session) { s.State = mimiry.SessionTerminated }, "has ended (terminated)"},
		{func(s *mimiry.Session) { s.State = mimiry.SessionTerminating }, "has ended (terminating)"},
		{func(s *mimiry.Session) { s.SSHEnabled = false }, "without SSH"},
		{func(s *mimiry.Session) { s.SSH = nil }, "without SSH"},
		{func(s *mimiry.Session) { s.SSH.HostKey = "" }, "before connections through the Mimiry gateway"},
	}
	for _, c := range cases {
		s := ready()
		c.change(s)
		if err := sshReady(s); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("err = %v, want %q", err, c.want)
		}
	}
}

func TestSessionStatusShowsTheHostKeyAsSSHDoes(t *testing.T) {
	f := newFakeAPI(t, "")
	got := hostKeyFingerprint(f.pub)
	if !strings.HasPrefix(got, "SHA256:") {
		t.Fatalf("fingerprint %q", got)
	}
}

// TestProxyCarriesStdinAndStdout runs `session proxy` against a rendezvous
// point that echoes what it receives, as the session's side would answer.
func TestProxyCarriesStdinAndStdout(t *testing.T) {
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/s/s-1/client" || r.Header.Get("Authorization") != "Bearer tkt" {
			t.Errorf("%s with %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(32 << 20) // as the relay and ssh-proxy accept
		ctx := r.Context()
		c.Write(ctx, websocket.MessageText, []byte("ready")) //nolint:errcheck
		var n int
		for {
			typ, msg, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				c.Write(ctx, websocket.MessageBinary, msg) //nolint:errcheck
				n += len(msg)
				continue
			}
			if strings.HasPrefix(string(msg), "fin ") {
				break
			}
		}
		c.Write(ctx, websocket.MessageText, fmt.Appendf(nil, "fin %d", n)) //nolint:errcheck
		c.Read(ctx)                                                         //nolint:errcheck // until closed
	}))
	defer echo.Close()

	f := newFakeAPI(t, fmt.Sprintf(`{"ticket":"tkt","expires_at":"2026-10-28T09:01:00Z","rendezvous":["ws%s"]}`,
		strings.TrimPrefix(echo.URL, "http")))
	var out, errOut bytes.Buffer
	in := "SSH-2.0-OpenSSH_9.6\r\n" + strings.Repeat("x", 100000)
	code := Run(context.Background(), []string{"--host", f.host, "--key", f.key, "session", "proxy", "mimiry-s-1"},
		strings.NewReader(in), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if out.String() != in {
		t.Fatalf("stdout carried %d bytes of %d", out.Len(), len(in))
	}
	if r := f.only(t); r.Method != http.MethodPost || r.Path != "/api/compute/v1/sessions/s-1/gateway-ticket" {
		t.Fatalf("asked %s %s", r.Method, r.Path)
	}
}
