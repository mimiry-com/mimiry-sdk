// Package mimiry is the client library for the Mimiry API.
//
// It is the one implementation of talking to the API: the mimiry CLI is built
// on it, and the Python package calls it through the CLI's machine interface.
// A fix here reaches all of them. It does no terminal I/O.
//
//	c, err := mimiry.New(mimiry.Options{Host: "platform", KeyPath: "~/.ssh/id_ed25519"})
//
// Authentication is an SSH-signature exchange for a short-lived access token,
// which the client caches and renews on its own.
package mimiry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mimiry-com/mimiry-sdk/internal/sshsig"
)

// signatureNamespace binds a signature to Mimiry token exchange; the server
// refuses any other.
const signatureNamespace = "mimiry-auth"

// maxResponseBody bounds how much of a response is read.
const maxResponseBody = 32 << 20

// Options configures a Client. Host is required; KeyPath or Signer is required
// for everything except the public endpoints.
type Options struct {
	// Host is a subdomain ("platform"), a hostname or a full URL. See ResolveBaseURL.
	Host string

	// KeyPath is the SSH private key, or its ".pub" when the key is held by
	// ssh-agent. A leading "~/" is expanded.
	KeyPath string
	// Signer is used instead of KeyPath when set.
	Signer ssh.Signer
	// Passphrase is asked for when the key is encrypted and not in ssh-agent.
	Passphrase func() ([]byte, error)

	// TokenStore keeps tokens between uses. Nil means an in-memory store, so
	// nothing is written to disk unless the caller asks for it.
	TokenStore TokenStore
	// TokenTTL is the lifetime to request for new tokens. Zero takes the
	// server's default; the server clamps any request to its own limits.
	TokenTTL time.Duration

	// HTTPClient is used for every request. Nil means a client with a
	// 60-second timeout.
	HTTPClient *http.Client
	// UserAgent is prepended to the library's own User-Agent.
	UserAgent string
}

// Client talks to one Mimiry host as one SSH key. It is safe for concurrent use.
type Client struct {
	baseURL     string
	signer      ssh.Signer
	fingerprint string
	store       TokenStore
	ttl         time.Duration
	http        *http.Client
	userAgent   string

	mu sync.Mutex // serialises sign-in, so concurrent callers share one exchange
}

// New returns a Client. It resolves the host and loads the key, but makes no
// request: the first call that needs a token signs in.
func New(o Options) (*Client, error) {
	base, err := ResolveBaseURL(o.Host)
	if err != nil {
		return nil, err
	}

	// No key is allowed: the public endpoints (the GPU catalogue and
	// availability) need none, and anything else answers ErrNoKey when called.
	signer := o.Signer
	if signer == nil && o.KeyPath != "" {
		path, err := expandHome(o.KeyPath)
		if err != nil {
			return nil, err
		}
		if signer, err = LoadSigner(path, o.Passphrase); err != nil {
			return nil, err
		}
	}

	c := &Client{
		baseURL:   base,
		signer:    signer,
		store:     o.TokenStore,
		ttl:       o.TokenTTL,
		http:      o.HTTPClient,
		userAgent: strings.TrimSpace(o.UserAgent + " mimiry-sdk-go/" + Version),
	}
	if signer != nil {
		c.fingerprint = ssh.FingerprintSHA256(signer.PublicKey())
	}
	if c.store == nil {
		c.store = &MemoryTokenStore{}
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 60 * time.Second}
	}
	return c, nil
}

// BaseURL is the API base URL this client resolved its host to.
func (c *Client) BaseURL() string { return c.baseURL }

// Fingerprint is the SHA256 fingerprint of the client's SSH key, or "" without one.
func (c *Client) Fingerprint() string { return c.fingerprint }

// AuthorizedKey is the client's SSH public key in authorized_keys form
// ("ssh-ed25519 AAAA…"), or "" without one. A session create can use it as the
// key that may log in to the machine.
func (c *Client) AuthorizedKey() string {
	if c.signer == nil {
		return ""
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(c.signer.PublicKey())))
}

// Token returns a usable access token, from the store or by signing in.
func (c *Client) Token(ctx context.Context) (*Token, error) {
	t, _, err := c.token(ctx, false)
	return t, err
}

// SignIn exchanges a fresh signature for a new token, whatever is cached.
func (c *Client) SignIn(ctx context.Context) (*Token, error) {
	t, _, err := c.token(ctx, true)
	return t, err
}

// SignOut forgets the cached token for this host and key.
func (c *Client) SignOut() error {
	if c.signer == nil {
		return nil
	}
	return c.store.Delete(c.baseURL, c.fingerprint)
}

// token reports whether the token was minted by this call (true) or came from
// the store (false).
func (c *Client) token(ctx context.Context, fresh bool) (*Token, bool, error) {
	if c.signer == nil {
		return nil, false, ErrNoKey
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if !fresh {
		t, err := c.store.Load(c.baseURL, c.fingerprint)
		if err != nil {
			return nil, false, err
		}
		if t.usable(time.Now()) {
			return t, false, nil
		}
	}
	t, err := c.exchange(ctx)
	if err != nil {
		return nil, false, err
	}
	if err := c.store.Save(t); err != nil {
		return nil, false, err
	}
	return t, true, nil
}

// exchange signs fingerprint\ntimestamp\nnonce and trades it for a token at
// POST /api/v1/auth/token.
func (c *Client) exchange(ctx context.Context) (*Token, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("mimiry: nonce: %w", err)
	}
	nonceHex := hex.EncodeToString(nonce)
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)

	message := []byte(c.fingerprint + "\n" + ts + "\n" + nonceHex)
	sig, err := sshsig.Sign(c.signer, signatureNamespace, message, sshsig.SHA512)
	if err != nil {
		return nil, fmt.Errorf("mimiry: sign in: %w", err)
	}

	var body any
	if c.ttl > 0 {
		body = map[string]int{"expires_in": int(c.ttl / time.Second)}
	}
	headers := http.Header{
		"X-SSH-Fingerprint": {c.fingerprint},
		"X-SSH-Signature":   {base64.StdEncoding.EncodeToString(sig)},
		"X-SSH-Timestamp":   {ts},
		"X-SSH-Nonce":       {nonceHex},
	}

	var resp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		UserID      string `json:"user_id"`
		Email       string `json:"email"`
	}
	if err := c.send(ctx, http.MethodPost, "/api/v1/auth/token", nil, body, headers, &resp); err != nil {
		return nil, err
	}
	if resp.AccessToken == "" || resp.ExpiresIn <= 0 {
		return nil, errors.New("mimiry: sign in: the server answered without a token")
	}
	return &Token{
		AccessToken: resp.AccessToken,
		ExpiresAt:   now.Add(time.Duration(resp.ExpiresIn) * time.Second),
		UserID:      resp.UserID,
		Email:       resp.Email,
		BaseURL:     c.baseURL,
		Fingerprint: c.fingerprint,
	}, nil
}

// Do sends an authenticated request and decodes a JSON response into out.
//
// path is relative to the host ("/api/compute/v1/sessions"). body, when
// non-nil, is sent as JSON. A response of 400 or above is an *APIError.
//
// A 401 with a cached token signs in once more and retries: the token may
// have been revoked or the server's signing key rotated since it was cached.
// A 401 with a token minted for this very call is returned as it is.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	t, minted, err := c.token(ctx, false)
	if err != nil {
		return err
	}
	err = c.send(ctx, method, path, query, body, bearer(t), out)
	var apiErr *APIError
	if minted || !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		return err
	}
	if t, err = c.SignIn(ctx); err != nil {
		return err
	}
	return c.send(ctx, method, path, query, body, bearer(t), out)
}

// DoPublic sends a request without a token, for the endpoints that need none.
func (c *Client) DoPublic(ctx context.Context, method, path string, query url.Values, out any) error {
	return c.send(ctx, method, path, query, nil, nil, out)
}

func bearer(t *Token) http.Header {
	return http.Header{"Authorization": {"Bearer " + t.AccessToken}}
}

func (c *Client) send(ctx context.Context, method, path string, query url.Values, body any, headers http.Header, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("mimiry: encode %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return fmt.Errorf("mimiry: %s %s: %w", method, path, err)
	}
	for k, v := range headers {
		req.Header[k] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return &TransportError{Method: method, Path: path, BaseURL: c.baseURL, Err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return fmt.Errorf("mimiry: %s %s: read response: %w", method, path, err)
	}

	if resp.StatusCode >= 400 {
		return newAPIError(method, path, resp.StatusCode, data)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("mimiry: %s %s: HTTP %d but the body is not the expected JSON (%s): %w",
			method, path, resp.StatusCode, truncate(strings.Join(strings.Fields(string(data)), " "), maxErrorBody), err)
	}
	return nil
}

func expandHome(p string) (string, error) {
	if p == "" {
		return "", ErrNoKey
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("mimiry: expand %s: %w", p, err)
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
	}
	return p, nil
}
