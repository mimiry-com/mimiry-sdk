package mimiry

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// writeKey writes an Ed25519 key pair to dir and returns the private key path.
// A non-empty passphrase encrypts it.
func writeKey(t *testing.T, dir, passphrase string) (string, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(pub), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, priv
}

// startAgent serves an ssh-agent holding keys on a socket and points
// SSH_AUTH_SOCK at it for the test.
func startAgent(t *testing.T, keys ...ed25519.PrivateKey) {
	t.Helper()
	ring := agent.NewKeyring()
	for _, k := range keys {
		if err := ring.Add(agent.AddedKey{PrivateKey: k}); err != nil {
			t.Fatal(err)
		}
	}
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go agent.ServeAgent(ring, conn)
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
}

func fingerprintOf(t *testing.T, k ed25519.PrivateKey) string {
	t.Helper()
	pub, err := ssh.NewPublicKey(k.Public())
	if err != nil {
		t.Fatal(err)
	}
	return ssh.FingerprintSHA256(pub)
}

func TestLoadSignerUnencrypted(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	path, priv := writeKey(t, t.TempDir(), "")
	s, err := LoadSigner(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ssh.FingerprintSHA256(s.PublicKey()), fingerprintOf(t, priv); got != want {
		t.Fatalf("fingerprint %s, want %s", got, want)
	}
}

func TestLoadSignerLockedKeyWithoutAgentOrPassphrase(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	path, _ := writeKey(t, t.TempDir(), "secret")
	if _, err := LoadSigner(path, nil); !errors.Is(err, ErrKeyLocked) {
		t.Fatalf("got %v, want ErrKeyLocked", err)
	}
}

func TestLoadSignerLockedKeyWithPassphrase(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	path, priv := writeKey(t, t.TempDir(), "secret")
	s, err := LoadSigner(path, func() ([]byte, error) { return []byte("secret"), nil })
	if err != nil {
		t.Fatal(err)
	}
	if ssh.FingerprintSHA256(s.PublicKey()) != fingerprintOf(t, priv) {
		t.Fatal("wrong key")
	}
	if _, err := LoadSigner(path, func() ([]byte, error) { return []byte("wrong"), nil }); err == nil {
		t.Fatal("a wrong passphrase unlocked the key")
	}
}

// A locked key held by ssh-agent is used through the agent, without asking.
func TestLoadSignerLockedKeyFromAgent(t *testing.T) {
	path, priv := writeKey(t, t.TempDir(), "secret")
	startAgent(t, priv)
	asked := false
	s, err := LoadSigner(path, func() ([]byte, error) { asked = true; return nil, errors.New("no") })
	if err != nil {
		t.Fatal(err)
	}
	if asked {
		t.Fatal("asked for a passphrase although the agent holds the key")
	}
	if ssh.FingerprintSHA256(s.PublicKey()) != fingerprintOf(t, priv) {
		t.Fatal("agent returned the wrong key")
	}
}

func TestLoadSignerPublicKeyNeedsAgent(t *testing.T) {
	path, priv := writeKey(t, t.TempDir(), "")
	_, other := writeKey(t, t.TempDir(), "")

	startAgent(t, other)
	if _, err := LoadSigner(path+".pub", nil); err == nil {
		t.Fatal("a .pub whose key the agent does not hold was accepted")
	}

	startAgent(t, priv)
	s, err := LoadSigner(path+".pub", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ssh.FingerprintSHA256(s.PublicKey()) != fingerprintOf(t, priv) {
		t.Fatal("wrong key")
	}
}

// An agent that is configured but unreachable is reported as such, not as a
// locked key.
func TestLoadSignerUnreachableAgent(t *testing.T) {
	path, _ := writeKey(t, t.TempDir(), "secret")
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(t.TempDir(), "nothing-here.sock"))
	_, err := LoadSigner(path, nil)
	if err == nil || errors.Is(err, ErrKeyLocked) {
		t.Fatalf("got %v, want an unreachable-agent error", err)
	}
}

func TestLoadSignerNoPath(t *testing.T) {
	if _, err := LoadSigner("", nil); !errors.Is(err, ErrNoKey) {
		t.Fatalf("got %v, want ErrNoKey", err)
	}
}
