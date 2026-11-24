package cli

// `session ssh` and `session proxy`: connecting to a session through the
// Mimiry gateway (RC-454). The proxy carries one SSH connection between
// stdin/stdout and the gateway; `ssh` runs OpenSSH with the proxy as its
// ProxyCommand and the session's published host key pinned, so the first
// connection is already checked and nothing is trusted on first use.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/mimiry-com/mimiry-sdk/cmdline"
	"github.com/mimiry-com/mimiry-sdk/mimiry"
)

func init() {
	s := groups["session"].cmds
	s["ssh"] = &command{
		summary: "Open a shell on a running session, over SSH",
		usage:   "<id> [-- <ssh options> [command]]",
		help:    sessionSSHHelp,
		run:     runSessionSSH,
	}
	s["proxy"] = &command{
		summary: "Carry one SSH connection to a session (an ssh ProxyCommand)",
		usage:   "<id>",
		help:    sessionProxyHelp,
		run:     runSessionProxy,
	}
}

const sessionSSHHelp = `
Runs ssh through the Mimiry gateway, checking that the session presents its
published host key. Anything after -- goes to ssh: options first, then a
command to run instead of a shell. ssh's exit status is this command's.

  mimiry session ssh <id>
  mimiry session ssh <id> -- -L 8888:localhost:8888
  mimiry session ssh <id> -- nvidia-smi`

const sessionProxyHelp = `
Used by 'mimiry session ssh'. To use your own ssh, scp or rsync, put this in
~/.ssh/config, then connect to "mimiry-<id>":

  Host mimiry-*
    ProxyCommand mimiry session proxy %n

with the session's SSH user (-l, from 'mimiry session status <id>'). The
first time, ssh shows the host key's fingerprint: accept it only if it is
the one 'mimiry session status <id>' shows.`

func runSessionProxy(e *env, _ *flag.FlagSet, args []string) error {
	id, err := oneID(args)
	if err != nil {
		return err
	}
	id = strings.TrimPrefix(id, hostAliasPrefix)
	c, err := e.client()
	if err != nil {
		return err
	}
	conn, err := c.Sessions().Connect(e.ctx, id)
	if err != nil {
		return err
	}
	// ssh's half: when it has sent everything, say so and keep receiving.
	go func() {
		io.Copy(conn, e.stdin) //nolint:errcheck // a failed send ends the connection, which the read reports
		conn.CloseWrite()      //nolint:errcheck // as above
	}()
	_, rerr := io.Copy(e.stdout, conn)
	return errors.Join(rerr, conn.Close())
}

// hostAliasPrefix names a session to ssh: the host key is recorded under
// "mimiry-<id>", and `session proxy` accepts the same name.
const hostAliasPrefix = "mimiry-"

func runSessionSSH(e *env, _ *flag.FlagSet, args []string) error {
	if len(args) == 0 || args[0] == "" {
		return usage("expected a session ID")
	}
	id, sshArgs := args[0], args[1:]
	opts, command := splitSSHArgs(sshArgs)

	c, err := e.client()
	if err != nil {
		return err
	}
	s, err := c.Sessions().Get(e.ctx, id, mimiry.GetSessionOptions{})
	if err != nil {
		return err
	}
	if err := sshReady(s); err != nil {
		return err
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return errors.New("ssh was not found: install OpenSSH and try again")
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find this program to use as ssh's ProxyCommand: %w", err)
	}

	// The pin: a known-hosts file holding only this session's key, removed
	// when ssh ends. Public, so a temporary file is fine; it is not kept
	// because a session's key means nothing after the session.
	kh, err := os.CreateTemp("", "mimiry-known-hosts-*")
	if err != nil {
		return err
	}
	defer os.Remove(kh.Name())
	alias := hostAliasPrefix + s.ID
	if _, err := fmt.Fprintf(kh, "%s %s\n", alias, strings.TrimSpace(s.SSH.HostKey)); err != nil {
		kh.Close()
		return err
	}
	if err := kh.Close(); err != nil {
		return err
	}

	key := ""
	if o, err := mimiry.ResolveOptions(e.host, e.key); err == nil {
		key = o.KeyPath
	}
	argv := sshCommandLine(sshInvocation{
		proxy:      proxyCommand(self, c.BaseURL(), e.key, s.ID),
		alias:      alias,
		knownHosts: kh.Name(),
		user:       s.SSH.Username,
		key:        key,
		opts:       opts,
		command:    command,
	})

	cmd := exec.Command(sshPath, argv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return cmdline.ExitCode(exit.ExitCode())
	}
	return err
}

// sshReady refuses, with what to do instead, a session ssh cannot reach.
func sshReady(s *mimiry.Session) error {
	switch s.State {
	case mimiry.SessionRunning, mimiry.SessionIdle:
	case mimiry.SessionSubmitted, mimiry.SessionProvisioned, mimiry.SessionBooting,
		mimiry.SessionSettingUp, mimiry.SessionPullingImage:
		return fmt.Errorf("session %s is still starting (%s). 'mimiry session wait %s' waits until it is ready", s.ID, s.State, s.ID)
	default:
		return fmt.Errorf("session %s has ended (%s)", s.ID, s.State)
	}
	if !s.SSHEnabled || s.SSH == nil || s.SSH.Username == "" {
		return fmt.Errorf("session %s was started without SSH", s.ID)
	}
	if s.SSH.HostKey == "" {
		return fmt.Errorf("session %s was started before connections through the Mimiry gateway; 'mimiry session status %s' shows how to reach it directly", s.ID, s.ID)
	}
	return nil
}

type sshInvocation struct {
	proxy, alias, knownHosts, user, key string
	opts, command                       []string
}

// sshCommandLine is ssh's arguments: ours, the caller's options, the host,
// then the caller's command.
func sshCommandLine(i sshInvocation) []string {
	argv := []string{
		"-o", "ProxyCommand=" + i.proxy,
		"-o", "HostKeyAlias=" + i.alias,
		"-o", "UserKnownHostsFile=" + i.knownHosts,
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UpdateHostKeys=no",
		"-o", "CheckHostIP=no",
		"-l", i.user,
	}
	// The key the CLI signed in with is the one a session lets in unless it
	// was started with another. Only that key is offered: ssh would first
	// try every key in the agent, and a server stops listening after a few
	// (2026-11-24: "too many authentication failures" with a full agent).
	// A caller who names a key with -i gets neither.
	if i.key != "" && !hasOption(i.opts, 'i') {
		argv = append(argv, "-i", i.key, "-o", "IdentitiesOnly=yes")
	}
	argv = append(argv, i.opts...)
	argv = append(argv, i.alias)
	return append(argv, i.command...)
}

// proxyCommand is the ProxyCommand line: this program, the host it signed in
// to, the key if one was named, and the session. ssh runs it through a shell
// and expands %-tokens in it, so every part is quoted and every % doubled.
func proxyCommand(self, host, key, id string) string {
	parts := []string{self, "--host", host}
	if key != "" {
		parts = append(parts, "--key", key)
	}
	parts = append(parts, "session", "proxy", id)
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(shellQuote(p), "%", "%%")
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if runtime.GOOS == "windows" {
		// Windows OpenSSH starts the ProxyCommand without a POSIX shell.
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sshValueOptions are ssh's options that take a value (OpenSSH's getopt
// string, ssh.c).
const sshValueOptions = "BbcDEeFIiJLlmOoPpQRSWw"

// splitSSHArgs separates ssh options from a command, as ssh does: options end
// at the first argument that is not one, or at "--".
func splitSSHArgs(args []string) (opts, command []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return opts, args[i+1:]
		}
		if len(a) < 2 || a[0] != '-' {
			return opts, args[i:]
		}
		opts = append(opts, a)
		// In a cluster such as -vL, a value option takes the rest of the
		// argument, or the next argument when it is last.
		for j := 1; j < len(a); j++ {
			if strings.IndexByte(sshValueOptions, a[j]) >= 0 {
				if j == len(a)-1 && i+1 < len(args) {
					i++
					opts = append(opts, args[i])
				}
				break
			}
		}
	}
	return opts, nil
}

// hasOption reports whether opts sets option o (as -o, or in a cluster).
func hasOption(opts []string, o byte) bool {
	for i := 0; i < len(opts); i++ {
		a := opts[i]
		if len(a) < 2 || a[0] != '-' {
			continue
		}
		for j := 1; j < len(a); j++ {
			if a[j] == o {
				return true
			}
			if strings.IndexByte(sshValueOptions, a[j]) >= 0 {
				if j == len(a)-1 {
					i++ // its value is the next argument
				}
				break
			}
		}
	}
	return false
}

// hostKeyFingerprint is the key as ssh shows it ("SHA256:…"), or the key
// itself if it cannot be read.
func hostKeyFingerprint(authorizedKey string) string {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorizedKey))
	if err != nil {
		return authorizedKey
	}
	return ssh.FingerprintSHA256(k)
}
