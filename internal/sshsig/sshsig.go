// Package sshsig produces OpenSSH SSHSIG signatures, the format written by
// `ssh-keygen -Y sign`.
//
// The Mimiry API authenticates a token exchange with an SSHSIG signature in the
// `mimiry-auth` namespace. Earlier clients shelled out to ssh-keygen for it,
// which made OpenSSH a runtime dependency (absent from slim container images)
// and put the message on disk in a temp file. This package does the same thing
// in-process.
//
// The format is specified in OpenSSH's PROTOCOL.sshsig. The output is armored
// exactly as ssh-keygen armors it, so for a deterministic key type (Ed25519)
// the bytes are identical to ssh-keygen's — the tests hold it to that.
package sshsig

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	magic   = "SSHSIG"
	version = 1

	// armorWidth is the line width ssh-keygen wraps the armored body at.
	armorWidth = 70
	armorBegin = "-----BEGIN SSH SIGNATURE-----"
	armorEnd   = "-----END SSH SIGNATURE-----"
)

// Hash names the digest applied to the message before it is signed.
type Hash string

const (
	SHA256 Hash = "sha256"
	SHA512 Hash = "sha512" // ssh-keygen's default
)

func (h Hash) sum(message []byte) ([]byte, error) {
	switch h {
	case SHA256:
		s := sha256.Sum256(message)
		return s[:], nil
	case SHA512:
		s := sha512.Sum512(message)
		return s[:], nil
	default:
		return nil, fmt.Errorf("sshsig: unsupported hash %q", h)
	}
}

// Sign signs message in namespace and returns the armored signature, the same
// bytes `ssh-keygen -Y sign -n <namespace>` writes to its .sig file.
func Sign(signer ssh.Signer, namespace string, message []byte, hash Hash) ([]byte, error) {
	if signer == nil {
		return nil, errors.New("sshsig: nil signer")
	}
	if namespace == "" {
		// An empty namespace is refused by the format: it is what binds a
		// signature to one purpose.
		return nil, errors.New("sshsig: empty namespace")
	}
	digest, err := hash.sum(message)
	if err != nil {
		return nil, err
	}

	signed := ssh.Marshal(struct {
		Namespace string
		Reserved  string
		Hash      string
		Digest    []byte
	}{namespace, "", string(hash), digest})
	signed = append([]byte(magic), signed...)

	sig, err := signWithPreferredAlgorithm(signer, signed)
	if err != nil {
		return nil, fmt.Errorf("sshsig: sign: %w", err)
	}

	blob := ssh.Marshal(struct {
		Version   uint32
		PublicKey []byte
		Namespace string
		Reserved  string
		Hash      string
		Signature []byte
	}{version, signer.PublicKey().Marshal(), namespace, "", string(hash), ssh.Marshal(sig)})
	blob = append([]byte(magic), blob...)

	return armor(blob), nil
}

// signWithPreferredAlgorithm signs RSA keys with rsa-sha2-512, as ssh-keygen
// does for SSHSIG. A plain Sign on an RSA key yields ssh-rsa (SHA-1), which
// current OpenSSH refuses to verify.
func signWithPreferredAlgorithm(signer ssh.Signer, data []byte) (*ssh.Signature, error) {
	if signer.PublicKey().Type() == ssh.KeyAlgoRSA {
		as, ok := signer.(ssh.AlgorithmSigner)
		if !ok {
			return nil, errors.New("RSA key cannot sign with rsa-sha2-512 (signer does not support algorithm selection)")
		}
		return as.SignWithAlgorithm(rand.Reader, data, ssh.KeyAlgoRSASHA512)
	}
	return signer.Sign(rand.Reader, data)
}

func armor(blob []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(blob)
	var b strings.Builder
	b.WriteString(armorBegin)
	b.WriteByte('\n')
	for len(enc) > armorWidth {
		b.WriteString(enc[:armorWidth])
		b.WriteByte('\n')
		enc = enc[armorWidth:]
	}
	b.WriteString(enc)
	b.WriteByte('\n')
	b.WriteString(armorEnd)
	b.WriteByte('\n')
	return []byte(b.String())
}
