package sshsig

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

// OpenSSH is the oracle: these tests hold the output to what ssh-keygen writes
// and accepts, not to a restatement of the format. They fail rather than skip
// when ssh-keygen is missing — a skipped signature test reads as a passing one.

const ns = "mimiry-auth"

func sshKeygen(t *testing.T, stdin []byte, args ...string) ([]byte, error) {
	t.Helper()
	path, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Fatalf("ssh-keygen is required as the test oracle: %v", err)
	}
	cmd := exec.Command(path, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	return cmd.CombinedOutput()
}

func newKey(t *testing.T, keyType string, bits string) (string, ssh.Signer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "id")
	args := []string{"-q", "-t", keyType, "-N", "", "-C", "", "-f", path}
	if bits != "" {
		args = append(args, "-b", bits)
	}
	if out, err := sshKeygen(t, nil, args...); err != nil {
		t.Fatalf("ssh-keygen -t %s: %v\n%s", keyType, err, out)
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		t.Fatal(err)
	}
	return path, signer
}

// verify asks ssh-keygen whether sig is a valid signature of message in namespace.
func verify(t *testing.T, sig, message []byte, namespace string) error {
	t.Helper()
	sigPath := filepath.Join(t.TempDir(), "msg.sig")
	if err := os.WriteFile(sigPath, sig, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := sshKeygen(t, message, "-Y", "check-novalidate", "-n", namespace, "-s", sigPath)
	if err != nil {
		return &oracleError{out: string(out), err: err}
	}
	return nil
}

type oracleError struct {
	out string
	err error
}

func (e *oracleError) Error() string { return e.err.Error() + ": " + e.out }

// The message shape the API signs: fingerprint, timestamp, nonce.
func message(signer ssh.Signer) []byte {
	return []byte(ssh.FingerprintSHA256(signer.PublicKey()) + "\n1793181241\n0123456789abcdef0123456789abcdef")
}

func TestOpenSSHVerifiesEveryKeyType(t *testing.T) {
	for _, tc := range []struct{ keyType, bits string }{
		{"ed25519", ""},
		{"ecdsa", "256"},
		{"ecdsa", "384"},
		{"ecdsa", "521"},
		{"rsa", "3072"},
	} {
		for _, h := range []Hash{SHA512, SHA256} {
			t.Run(tc.keyType+tc.bits+"/"+string(h), func(t *testing.T) {
				_, signer := newKey(t, tc.keyType, tc.bits)
				msg := message(signer)
				sig, err := Sign(signer, ns, msg, h)
				if err != nil {
					t.Fatal(err)
				}
				if err := verify(t, sig, msg, ns); err != nil {
					t.Fatalf("ssh-keygen rejected our signature: %v", err)
				}
			})
		}
	}
}

// Ed25519 signatures are deterministic, so the whole armored file must match
// ssh-keygen byte for byte: framing, field order, hash name and line wrapping.
func TestEd25519MatchesSSHKeygenByteForByte(t *testing.T) {
	keyPath, signer := newKey(t, "ed25519", "")
	msg := message(signer)

	msgPath := filepath.Join(t.TempDir(), "msg")
	if err := os.WriteFile(msgPath, msg, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := sshKeygen(t, nil, "-Y", "sign", "-f", keyPath, "-n", ns, msgPath); err != nil {
		t.Fatalf("ssh-keygen -Y sign: %v\n%s", err, out)
	}
	want, err := os.ReadFile(msgPath + ".sig")
	if err != nil {
		t.Fatal(err)
	}

	got, err := Sign(signer, ns, msg, SHA512)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("armored signature differs from ssh-keygen's\n got:\n%s\nwant:\n%s", got, want)
	}
}

// The oracle is not vacuous: it refuses a signature bound to another namespace
// and one over a different message.
func TestOpenSSHRejectsWrongNamespaceAndMessage(t *testing.T) {
	_, signer := newKey(t, "ed25519", "")
	msg := message(signer)
	sig, err := Sign(signer, ns, msg, SHA512)
	if err != nil {
		t.Fatal(err)
	}
	if verify(t, sig, msg, "file") == nil {
		t.Fatal("a mimiry-auth signature verified in namespace \"file\"")
	}
	if verify(t, sig, append(msg, 'x'), ns) == nil {
		t.Fatal("the signature verified over a different message")
	}
}

func TestRefusesEmptyNamespaceAndUnknownHash(t *testing.T) {
	_, signer := newKey(t, "ed25519", "")
	if _, err := Sign(signer, "", []byte("m"), SHA512); err == nil {
		t.Error("empty namespace accepted")
	}
	if _, err := Sign(signer, ns, []byte("m"), Hash("md5")); err == nil {
		t.Error("unknown hash accepted")
	}
}
