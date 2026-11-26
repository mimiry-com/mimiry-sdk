package mimiry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// sshServer is a session's SSH server in miniature: it lets in one key,
// runs exec requests with `sh -c` as ssh-proxy does, and serves sftp.
type sshServer struct {
	t       *testing.T
	hostKey ssh.Signer
	user    ssh.PublicKey
	// dropExec closes the connection instead of answering an exec.
	dropExec bool
}

func newSSHServer(t *testing.T, user ssh.PublicKey) *sshServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hk, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return &sshServer{t: t, hostKey: hk, user: user}
}

func (s *sshServer) authorizedHostKey() string {
	return string(ssh.MarshalAuthorizedKey(s.hostKey.PublicKey()))
}

// serve answers one connection.
func (s *sshServer) serve(conn net.Conn) {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(k.Marshal(), s.user.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("not this key")
		},
	}
	cfg.AddHostKey(s.hostKey)
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, err := nc.Accept()
		if err != nil {
			return
		}
		go s.channel(sc, ch, creqs)
	}
}

func (s *sshServer) channel(sc *ssh.ServerConn, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "exec":
			var p struct{ C string }
			ssh.Unmarshal(req.Payload, &p) //nolint:errcheck // a test server
			req.Reply(true, nil)           //nolint:errcheck
			if s.dropExec {
				sc.Close()
				return
			}
			cmd := exec.Command("sh", "-c", p.C)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = ch, ch, ch.Stderr()
			code := 0
			if err := cmd.Run(); err != nil {
				var ee *exec.ExitError
				if !errors.As(err, &ee) {
					s.t.Errorf("exec: %v", err)
				}
				code = ee.ExitCode()
			}
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)})) //nolint:errcheck
			return
		case "subsystem":
			req.Reply(true, nil) //nolint:errcheck
			srv, err := sftp.NewServer(ch)
			if err != nil {
				s.t.Error(err)
				return
			}
			srv.Serve() //nolint:errcheck // ends with the channel
			return
		default:
			req.Reply(false, nil) //nolint:errcheck
		}
	}
}

// dialTest connects to srv over loopback TCP, pinning pinned. Not net.Pipe:
// both ends send their version line first, and a pipe without a buffer
// deadlocks there.
func dialTest(t *testing.T, srv *sshServer, pinned string, signer ssh.Signer) (*SessionSSH, error) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		if b, err := l.Accept(); err == nil {
			srv.serve(b)
		}
	}()
	a, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	x, err := dialSSH(context.Background(), a, "s-1", "root", pinned, signer)
	if err == nil {
		t.Cleanup(func() { x.Close() })
	} else {
		a.Close()
	}
	return x, err
}

func testSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestExecPassesEveryArgumentLiterally(t *testing.T) {
	user := testSigner(t)
	srv := newSSHServer(t, user.PublicKey())
	x, err := dialTest(t, srv, srv.authorizedHostKey(), user)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"two words", "it's", `"double"`, "$(echo pwned)", "`id`", "a;b", "", "*", "\\n"}
	var out bytes.Buffer
	code, err := x.Exec(context.Background(), append([]string{"printf", "%s\n"}, args...), ExecIO{Stdout: &out})
	if err != nil || code != 0 {
		t.Fatalf("code %d, err %v", code, err)
	}
	if got, want := out.String(), strings.Join(args, "\n")+"\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExecReturnsTheExitStatusAndBothStreams(t *testing.T) {
	user := testSigner(t)
	srv := newSSHServer(t, user.PublicKey())
	x, err := dialTest(t, srv, srv.authorizedHostKey(), user)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code, err := x.Exec(context.Background(), []string{"sh", "-c", "cat; echo oops >&2; exit 7"},
		ExecIO{Stdin: strings.NewReader("input\n"), Stdout: &out, Stderr: &errOut})
	if err != nil {
		t.Fatal(err)
	}
	if code != 7 || out.String() != "input\n" || errOut.String() != "oops\n" {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
}

func TestExecReportsALostConnectionAsAnErrorNotSuccess(t *testing.T) {
	user := testSigner(t)
	srv := newSSHServer(t, user.PublicKey())
	srv.dropExec = true
	x, err := dialTest(t, srv, srv.authorizedHostKey(), user)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.Exec(context.Background(), []string{"true"}, ExecIO{}); err == nil {
		t.Fatal("a command whose exit status never came reported success")
	}
}

func TestDialRefusesAServerWithAnotherHostKey(t *testing.T) {
	user := testSigner(t)
	srv := newSSHServer(t, user.PublicKey())
	impostor := newSSHServer(t, user.PublicKey())
	if _, err := dialTest(t, impostor, srv.authorizedHostKey(), user); err == nil {
		t.Fatal("connected to a server presenting a key other than the pinned one")
	}
}

func TestDialRefusesAnUnreadablePin(t *testing.T) {
	user := testSigner(t)
	srv := newSSHServer(t, user.PublicKey())
	if _, err := dialTest(t, srv, "not a key", user); err == nil {
		t.Fatal("connected without a host key to check")
	}
}

func TestUploadAndDownloadRoundTrip(t *testing.T) {
	user := testSigner(t)
	srv := newSSHServer(t, user.PublicKey())
	x, err := dialTest(t, srv, srv.authorizedHostKey(), user)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	remote := filepath.Join(dir, "payload.bin")
	want := make([]byte, 5<<20+3)
	rand.Read(want)
	n, err := x.Upload(context.Background(), bytes.NewReader(want), remote, 0o600)
	if err != nil || n != int64(len(want)) {
		t.Fatalf("upload: %d bytes, %v", n, err)
	}
	st, err := os.Stat(remote)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*mimiry-*")); len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
	var got bytes.Buffer
	if n, err := x.Download(context.Background(), remote, &got); err != nil || n != int64(len(want)) {
		t.Fatalf("download: %d bytes, %v", n, err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatal("the file came back different")
	}
}

func TestUploadLeavesNothingWhenTheSourceFails(t *testing.T) {
	user := testSigner(t)
	srv := newSSHServer(t, user.PublicKey())
	x, err := dialTest(t, srv, srv.authorizedHostKey(), user)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	remote := filepath.Join(dir, "f")
	src := io.MultiReader(strings.NewReader("half"), iotestErrReader{})
	if _, err := x.Upload(context.Background(), src, remote, 0o600); err == nil {
		t.Fatal("a failed read was uploaded as a whole file")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}

type iotestErrReader struct{}

func (iotestErrReader) Read([]byte) (int, error) { return 0, errors.New("disk gone") }

func TestDownloadRefusesADirectory(t *testing.T) {
	user := testSigner(t)
	srv := newSSHServer(t, user.PublicKey())
	x, err := dialTest(t, srv, srv.authorizedHostKey(), user)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.Download(context.Background(), t.TempDir(), io.Discard); err == nil {
		t.Fatal("downloaded a directory")
	}
}

func TestDialRefusesASessionWithoutAPublishedHostKey(t *testing.T) {
	cases := map[string]string{
		"before the gateway": `{"id":"s-1","state":"running","ssh_enabled":true,"ssh":{"host":"1.2.3.4","port":22,"username":"root"}}`,
		"not running":        `{"id":"s-1","state":"booting","ssh_enabled":true,"ssh":{"port":22,"username":"root","host_key":"ssh-ed25519 AAAA"}}`,
		"without SSH":        `{"id":"s-1","state":"running","ssh_enabled":false}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			c := computeClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/compute/v1/sessions/s-1" {
					t.Errorf("asked for %s %s: only the session may be read", r.Method, r.URL.Path)
				}
				io.WriteString(w, body) //nolint:errcheck
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := c.Sessions().Dial(ctx, "s-1", DialOptions{}); !errors.Is(err, ErrSessionNotReachable) {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func TestDownloadFileKeepsTheLocalFileRules(t *testing.T) {
	user := testSigner(t)
	srv := newSSHServer(t, user.PublicKey())
	x, err := dialTest(t, srv, srv.authorizedHostKey(), user)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	remoteDir, local := t.TempDir(), t.TempDir()
	remote := filepath.Join(remoteDir, "result")
	if err := os.WriteFile(remote, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(local, "out")
	if _, err := x.DownloadFile(ctx, remote, dst, false); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dst); st.Mode().Perm() != 0o600 {
		t.Fatalf("created %v, want 0600", st.Mode().Perm())
	}

	if err := os.WriteFile(dst, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := x.DownloadFile(ctx, remote, dst, false); !errors.Is(err, ErrLocalFileExists) {
		t.Fatalf("an existing file was not refused: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "mine" {
		t.Fatalf("the existing file changed to %q", b)
	}

	// A link where the file would go: refused, and with overwrite replaced
	// itself, never followed to what it points at.
	victim := filepath.Join(local, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(local, "link")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if _, err := x.DownloadFile(ctx, remote, link, false); !errors.Is(err, ErrLocalFileExists) {
		t.Fatalf("a link in place was not refused: %v", err)
	}
	if _, err := x.DownloadFile(ctx, remote, link, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("the download wrote through the link: %q", b)
	}
	if st, _ := os.Lstat(link); st.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the link is still there")
	}

	if _, err := x.DownloadFile(ctx, filepath.Join(remoteDir, "missing"), filepath.Join(local, "never"), false); err == nil {
		t.Fatal("a missing remote file downloaded")
	}
	if _, err := os.Lstat(filepath.Join(local, "never")); err == nil {
		t.Fatal("a failed download left a file behind")
	}
	if left, _ := filepath.Glob(filepath.Join(local, ".*mimiry-*")); len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
}

func TestUploadFileRefusesWhatIsNotARegularFile(t *testing.T) {
	user := testSigner(t)
	srv := newSSHServer(t, user.PublicKey())
	x, err := dialTest(t, srv, srv.authorizedHostKey(), user)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.UploadFile(context.Background(), t.TempDir(), filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("uploaded a directory")
	}
}
