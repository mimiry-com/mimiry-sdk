package mimiry

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// ErrNoKey is returned when no SSH key was given.
var ErrNoKey = errors.New("mimiry: no SSH key configured (set Options.KeyPath or MIMIRY_SSH_KEY)")

// ErrKeyLocked is returned when the private key is passphrase-protected, it is
// not loaded in ssh-agent, and no passphrase source was given.
var ErrKeyLocked = errors.New("mimiry: the SSH key is passphrase-protected; load it into ssh-agent (ssh-add) or supply a passphrase")

// ErrKeyUnavailable wraps every other failure to obtain a signer: an
// unreadable or unparseable key file, an unreachable ssh-agent, a public key
// the agent does not hold, a wrong passphrase.
var ErrKeyUnavailable = errors.New("mimiry: SSH key unavailable")

// LoadSigner returns a signer for the key at path.
//
// path may name the private key or its ".pub" file. The order is the one
// OpenSSH users expect:
//
//  1. an unencrypted private key is used directly;
//  2. otherwise the matching key is taken from ssh-agent, if one is running
//     and holds it — the only route when path names a ".pub" file;
//  3. otherwise passphrase is asked for, if non-nil.
//
// passphrase is how a caller with a person in front of it (the CLI) prompts;
// a library caller without one passes nil and gets ErrKeyLocked.
func LoadSigner(path string, passphrase func() ([]byte, error)) (ssh.Signer, error) {
	if path == "" {
		return nil, ErrNoKey
	}

	if strings.HasSuffix(path, ".pub") {
		pub, err := readPublicKey(path)
		if err != nil {
			return nil, err
		}
		s, err := agentSigner(pub)
		if err != nil {
			return nil, err
		}
		if s == nil {
			return nil, fmt.Errorf("%w: %s names a public key, and ssh-agent does not hold its private key", ErrKeyUnavailable, path)
		}
		return s, nil
	}

	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read SSH key: %w", ErrKeyUnavailable, err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err == nil {
		return signer, nil
	}

	var missing *ssh.PassphraseMissingError
	if !errors.As(err, &missing) {
		return nil, fmt.Errorf("%w: parse SSH key %s: %w", ErrKeyUnavailable, path, err)
	}

	pub := missing.PublicKey
	if pub == nil {
		// Older key formats do not carry the public key in the clear.
		pub, _ = readPublicKey(path + ".pub")
	}
	if pub != nil {
		s, err := agentSigner(pub)
		if err != nil {
			return nil, err
		}
		if s != nil {
			return s, nil
		}
	}

	if passphrase == nil {
		return nil, ErrKeyLocked
	}
	pass, err := passphrase()
	if err != nil {
		return nil, fmt.Errorf("%w: passphrase: %w", ErrKeyUnavailable, err)
	}
	signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, pass)
	if err != nil {
		return nil, fmt.Errorf("%w: unlock SSH key %s: %w", ErrKeyUnavailable, path, err)
	}
	return signer, nil
}

func readPublicKey(path string) (ssh.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read public key: %w", ErrKeyUnavailable, err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		return nil, fmt.Errorf("%w: parse public key %s: %w", ErrKeyUnavailable, path, err)
	}
	return pub, nil
}

// agentSigner returns ssh-agent's signer for pub, or nil when no agent is
// running or it does not hold the key. An agent that is configured but cannot
// be reached is an error, not "no key": it would otherwise be reported as a
// locked key, which sends the user after the wrong problem.
func agentSigner(pub ssh.PublicKey) (ssh.Signer, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, nil
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("%w: SSH_AUTH_SOCK is set but ssh-agent is unreachable: %w", ErrKeyUnavailable, err)
	}
	signers, err := agent.NewClient(conn).Signers()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: list ssh-agent keys: %w", ErrKeyUnavailable, err)
	}
	want := pub.Marshal()
	for _, s := range signers {
		if bytes.Equal(s.PublicKey().Marshal(), want) {
			// The connection stays open for the signer's lifetime; the
			// process is short-lived or holds one client, so it is not pooled.
			return s, nil
		}
	}
	conn.Close()
	return nil, nil
}
