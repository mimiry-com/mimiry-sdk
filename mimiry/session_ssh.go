package mimiry

// Running commands in a session and copying files in and out (RC-473).
//
// Everything here goes over one SSH connection through the gateway
// (Connect), signed in with the client's key and checked against the
// session's published host key. It is what `mimiry session ssh` already
// allows, used from a program: no new access, and no other way in. A session
// without a published host key is refused; nothing falls back to its address
// or to trusting the first key it shows.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// DialOptions configures Dial.
type DialOptions struct {
	// Signer signs in to the session's SSH server. Nil means the client's
	// own key, the one a session lets in unless it was started with another.
	Signer ssh.Signer
}

// SessionSSH is an SSH connection to a running session. Exec runs commands
// in its container; Upload and Download copy files. It is safe for
// concurrent use: each call opens its own channel on the one connection.
type SessionSSH struct {
	id     string
	client *ssh.Client

	sftpMu sync.Mutex
	sftp   *sftp.Client
}

// Dial opens an SSH connection to a running session through the gateway.
// Close it when done.
func (s *SessionsService) Dial(ctx context.Context, id string, o DialOptions) (*SessionSSH, error) {
	signer := o.Signer
	if signer == nil {
		signer = s.c.signer
	}
	if signer == nil {
		return nil, ErrNoKey
	}
	sess, err := s.Get(ctx, id, GetSessionOptions{})
	if err != nil {
		return nil, err
	}
	if err := sshTarget(sess); err != nil {
		return nil, err
	}
	conn, err := s.Connect(ctx, sess.ID)
	if err != nil {
		return nil, err
	}
	x, err := dialSSH(ctx, conn, sess.ID, sess.SSH.Username, sess.SSH.HostKey, signer)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return x, nil
}

// ErrSessionNotReachable is returned by Dial for a session SSH cannot reach:
// not running, started without SSH, or started before the gateway.
var ErrSessionNotReachable = errors.New("mimiry: the session cannot be reached over SSH")

func sshTarget(s *Session) error {
	switch s.State {
	case SessionRunning, SessionIdle:
	default:
		return fmt.Errorf("%w: session %s is %s, not running", ErrSessionNotReachable, s.ID, s.State)
	}
	if !s.SSHEnabled || s.SSH == nil || s.SSH.Username == "" {
		return fmt.Errorf("%w: session %s was started without SSH", ErrSessionNotReachable, s.ID)
	}
	if strings.TrimSpace(s.SSH.HostKey) == "" {
		return fmt.Errorf("%w: session %s publishes no host key (started before the gateway)", ErrSessionNotReachable, s.ID)
	}
	return nil
}

// dialSSH speaks SSH over conn, accepting only hostKey.
func dialSSH(ctx context.Context, conn net.Conn, id, user, hostKey string, signer ssh.Signer) (*SessionSSH, error) {
	pinned, _, _, _, err := ssh.ParseAuthorizedKey([]byte(hostKey))
	if err != nil {
		return nil, fmt.Errorf("mimiry: the session's published host key cannot be read: %w", err)
	}
	cfg := &ssh.ClientConfig{
		User:              user,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback:   ssh.FixedHostKey(pinned),
		HostKeyAlgorithms: hostKeyAlgorithms(pinned),
	}

	// The handshake has no context of its own: closing the connection is what
	// stops it when ctx ends first.
	type result struct {
		c     ssh.Conn
		chans <-chan ssh.NewChannel
		reqs  <-chan *ssh.Request
		err   error
	}
	done := make(chan result, 1)
	go func() {
		c, chans, reqs, err := ssh.NewClientConn(conn, hostAliasPrefix+id, cfg)
		done <- result{c, chans, reqs, err}
	}()
	select {
	case <-ctx.Done():
		conn.Close()
		<-done
		return nil, ctx.Err()
	case r := <-done:
		if r.err != nil {
			return nil, fmt.Errorf("mimiry: SSH to session %s: %w", id, r.err)
		}
		return &SessionSSH{id: id, client: ssh.NewClient(r.c, r.chans, r.reqs)}, nil
	}
}

// hostAliasPrefix is the name a session's host key is checked under:
// "mimiry-<id>", the same name `mimiry session ssh` gives ssh.
const hostAliasPrefix = "mimiry-"

// hostKeyAlgorithms asks the server for the pinned key's own algorithm, so it
// cannot offer another key it also holds and fail the check confusingly.
func hostKeyAlgorithms(k ssh.PublicKey) []string {
	if k.Type() == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	}
	return []string{k.Type()}
}

// Close closes the connection and everything open on it.
func (x *SessionSSH) Close() error {
	x.sftpMu.Lock()
	if x.sftp != nil {
		x.sftp.Close()
		x.sftp = nil
	}
	x.sftpMu.Unlock()
	return x.client.Close()
}

// ExecIO is where a command's input comes from and its output goes. A nil
// Stdin sends nothing; a nil Stdout or Stderr discards.
type ExecIO struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

// ErrConnectionLost is returned when the connection ended before the
// command's exit status arrived: the command may still be running, or may
// have finished either way.
var ErrConnectionLost = errors.New("mimiry: the connection ended before the command reported its exit status")

// Exec runs a command in the session's container and returns its exit
// status. argv is the program and its arguments, passed as they are: each is
// quoted for the container's shell, so spaces, quotes and $ arrive unchanged.
// To use the shell, run it: []string{"sh", "-c", "a | b"}.
//
// A command ended by a signal returns an error naming it. Cancelling ctx
// ends the command's channel and returns ctx's error.
func (x *SessionSSH) Exec(ctx context.Context, argv []string, o ExecIO) (int, error) {
	if len(argv) == 0 || argv[0] == "" {
		return 0, errors.New("mimiry: Exec needs a program to run")
	}
	s, err := x.client.NewSession()
	if err != nil {
		return 0, fmt.Errorf("mimiry: open a channel to session %s: %w", x.id, err)
	}
	defer s.Close()
	s.Stdin, s.Stdout, s.Stderr = o.Stdin, o.Stdout, o.Stderr
	if s.Stdout == nil {
		s.Stdout = io.Discard
	}
	if s.Stderr == nil {
		s.Stderr = io.Discard
	}

	done := make(chan error, 1)
	go func() { done <- s.Run(shellJoin(argv)) }()
	select {
	case <-ctx.Done():
		s.Close()
		return 0, ctx.Err()
	case err = <-done:
	}

	var exit *ssh.ExitError
	var missing *ssh.ExitMissingError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exit):
		if sig := exit.Signal(); sig != "" {
			return exit.ExitStatus(), fmt.Errorf("mimiry: the command was ended by signal %s", sig)
		}
		return exit.ExitStatus(), nil
	case errors.As(err, &missing), errors.Is(err, io.EOF):
		return 0, ErrConnectionLost
	default:
		return 0, fmt.Errorf("mimiry: exec in session %s: %w", x.id, err)
	}
}

// shellJoin is argv as one POSIX shell line: every argument in single
// quotes, so the shell takes each as one literal word.
func shellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

// sftpClient opens the connection's file channel on first use. The session's
// SSH server serves it from inside the container.
func (x *SessionSSH) sftpClient() (*sftp.Client, error) {
	x.sftpMu.Lock()
	defer x.sftpMu.Unlock()
	if x.sftp == nil {
		c, err := sftp.NewClient(x.client)
		if err != nil {
			return nil, fmt.Errorf("mimiry: open file transfer to session %s: %w", x.id, err)
		}
		x.sftp = c
	}
	return x.sftp, nil
}

// withContext runs f, and closes the file channel if ctx ends first so f
// returns. The next call opens a new one.
func (x *SessionSSH) withContext(ctx context.Context, f func(*sftp.Client) error) error {
	c, err := x.sftpClient()
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- f(c) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		x.sftpMu.Lock()
		if x.sftp == c {
			c.Close()
			x.sftp = nil
		}
		x.sftpMu.Unlock()
		<-done
		return ctx.Err()
	}
}

// Upload writes r to remotePath in the container, with mode's permission
// bits. The file appears complete or not at all: the data goes to a
// temporary name beside it, which is renamed over remotePath at the end.
func (x *SessionSSH) Upload(ctx context.Context, r io.Reader, remotePath string, mode os.FileMode) (int64, error) {
	if remotePath == "" {
		return 0, errors.New("mimiry: Upload needs a path in the session")
	}
	var n int64
	err := x.withContext(ctx, func(c *sftp.Client) error {
		var rnd [6]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return err
		}
		tmp := path.Join(path.Dir(remotePath), "."+path.Base(remotePath)+".mimiry-"+hex.EncodeToString(rnd[:]))
		f, err := c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil {
			return fmt.Errorf("mimiry: create %s in session %s: %w", remotePath, x.id, err)
		}
		ok := false
		defer func() {
			if !ok {
				f.Close()
				c.Remove(tmp)
			}
		}()
		if err := f.Chmod(mode.Perm()); err != nil {
			return fmt.Errorf("mimiry: set the mode of %s in session %s: %w", remotePath, x.id, err)
		}
		if n, err = f.ReadFrom(r); err != nil {
			return fmt.Errorf("mimiry: write %s in session %s: %w", remotePath, x.id, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("mimiry: write %s in session %s: %w", remotePath, x.id, err)
		}
		if err := c.PosixRename(tmp, remotePath); err != nil {
			return fmt.Errorf("mimiry: put %s in place in session %s: %w", remotePath, x.id, err)
		}
		ok = true
		return nil
	})
	return n, err
}

// Download copies remotePath from the container to w and returns the number
// of bytes copied. Only a regular file is copied.
func (x *SessionSSH) Download(ctx context.Context, remotePath string, w io.Writer) (int64, error) {
	if remotePath == "" {
		return 0, errors.New("mimiry: Download needs a path in the session")
	}
	var n int64
	err := x.withContext(ctx, func(c *sftp.Client) error {
		f, err := c.Open(remotePath)
		if err != nil {
			return fmt.Errorf("mimiry: open %s in session %s: %w", remotePath, x.id, err)
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return fmt.Errorf("mimiry: read %s in session %s: %w", remotePath, x.id, err)
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("mimiry: %s in session %s is not a regular file", remotePath, x.id)
		}
		if n, err = f.WriteTo(w); err != nil {
			return fmt.Errorf("mimiry: read %s in session %s: %w", remotePath, x.id, err)
		}
		return nil
	})
	return n, err
}
