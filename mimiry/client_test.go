package mimiry

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeAPI checks a sign-in the way the API does — the four headers, the
// timestamp window, nonce reuse, and the signature over
// fingerprint\ntimestamp\nnonce in the mimiry-auth namespace — using
// `ssh-keygen -Y verify` against the registered key as the judge.
type fakeAPI struct {
	t           *testing.T
	allowed     string // allowed_signers file holding the registered key
	fingerprint string

	exchanges atomic.Int32
	lastTTL   atomic.Int32
	nonces    sync.Map

	mu      sync.Mutex
	valid   map[string]bool // tokens the API currently accepts
	handler http.HandlerFunc
}

func newFakeAPI(t *testing.T, registered ed25519.PrivateKey) (*fakeAPI, *httptest.Server) {
	t.Helper()
	pub, err := ssh.NewPublicKey(registered.Public())
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(t.TempDir(), "allowed_signers")
	line := "user@test " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + "\n"
	if err := os.WriteFile(allowed, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeAPI{t: t, allowed: allowed, fingerprint: ssh.FingerprintSHA256(pub), valid: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeAPI) revokeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.valid = map[string]bool{}
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/auth/token" {
		f.exchange(w, r)
		return
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	// As on the API, the catalogue and availability are public.
	ok := f.valid[tok] || r.URL.Path == "/api/compute/v1/availability" || r.URL.Path == "/api/compute/v1/catalog"
	h := f.handler
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"Invalid or expired token"}`)
		return
	}
	h(w, r)
}

func (f *fakeAPI) exchange(w http.ResponseWriter, r *http.Request) {
	refuse := func(why string) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": why})
	}
	fp, sigB64, ts, nonce := r.Header.Get("X-SSH-Fingerprint"), r.Header.Get("X-SSH-Signature"),
		r.Header.Get("X-SSH-Timestamp"), r.Header.Get("X-SSH-Nonce")
	if fp == "" || sigB64 == "" || ts == "" || nonce == "" {
		refuse("Missing required SSH authentication headers")
		return
	}
	if fp != f.fingerprint {
		refuse("SSH signature verification failed") // key not registered
		return
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(sec, 0)).Abs() > 5*time.Minute {
		refuse("Invalid timestamp")
		return
	}
	if _, seen := f.nonces.LoadOrStore(nonce, true); seen {
		refuse("SSH signature verification failed") // replay
		return
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		refuse("SSH signature verification failed")
		return
	}
	sigPath := filepath.Join(f.t.TempDir(), "sig")
	if err := os.WriteFile(sigPath, sig, 0o600); err != nil {
		f.t.Error(err)
		return
	}
	cmd := exec.Command("ssh-keygen", "-Y", "verify", "-f", f.allowed, "-I", "user@test", "-n", "mimiry-auth", "-s", sigPath)
	cmd.Stdin = strings.NewReader(fp + "\n" + ts + "\n" + nonce)
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Logf("ssh-keygen -Y verify refused the signature: %s", out)
		refuse("SSH signature verification failed")
		return
	}

	// The real server takes expires_in as optional and clamps any value it is
	// given, so an explicit 0 is not "the default": it is a request for the
	// shortest token allowed. Absent is recorded as -1 to tell them apart.
	var req struct {
		ExpiresIn *int `json:"expires_in"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	ttl := 300
	f.lastTTL.Store(-1)
	if req.ExpiresIn != nil {
		f.lastTTL.Store(int32(*req.ExpiresIn))
		ttl = *req.ExpiresIn
	}
	n := f.exchanges.Add(1)
	tok := fmt.Sprintf("token-%d", n)
	f.mu.Lock()
	f.valid[tok] = true
	f.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{
		"access_token": tok, "token_type": "Bearer", "expires_in": ttl,
		"user_id": "u-1", "email": "user@example.com",
	})
}

func newTestClient(t *testing.T, srv *httptest.Server, key ed25519.PrivateKey, store TokenStore) *Client {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{Host: srv.URL, Signer: signer, TokenStore: store})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func genKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSignInIsAcceptedAndTheTokenIsReused(t *testing.T) {
	key := genKey(t)
	api, srv := newFakeAPI(t, key)
	api.handler = func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"sessions":[],"total_count":0}`)
	}
	c := newTestClient(t, srv, key, nil)

	for i := 0; i < 3; i++ {
		var out struct {
			TotalCount int `json:"total_count"`
		}
		if err := c.Do(context.Background(), http.MethodGet, "/api/compute/v1/sessions", nil, nil, &out); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if n := api.exchanges.Load(); n != 1 {
		t.Fatalf("%d sign-ins for 3 calls, want 1", n)
	}
	if ttl := api.lastTTL.Load(); ttl != -1 {
		t.Fatalf("sent expires_in=%d with no TokenTTL set, want it omitted", ttl)
	}
}

func TestTokenTTLIsRequested(t *testing.T) {
	key := genKey(t)
	api, srv := newFakeAPI(t, key)
	signer, _ := ssh.NewSignerFromKey(key)
	c, err := New(Options{Host: srv.URL, Signer: signer, TokenTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := c.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := api.lastTTL.Load(); got != 3600 {
		t.Fatalf("requested expires_in=%d, want 3600", got)
	}
	if d := time.Until(tok.ExpiresAt); d < 59*time.Minute || d > 61*time.Minute {
		t.Fatalf("token expires in %v, want about an hour", d)
	}
}

// A key the API does not know is refused, and the server's words come through.
func TestUnregisteredKeyIsRefused(t *testing.T) {
	_, srv := newFakeAPI(t, genKey(t))
	c := newTestClient(t, srv, genKey(t), nil)
	_, err := c.Token(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 401 || apiErr.ErrorCode != "SSH signature verification failed" {
		t.Fatalf("got %v, want the API's 401", err)
	}
}

// A cached token the API no longer accepts is replaced once, and the call succeeds.
func TestRevokedCachedTokenIsRenewedOnce(t *testing.T) {
	key := genKey(t)
	api, srv := newFakeAPI(t, key)
	api.handler = func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) }
	c := newTestClient(t, srv, key, nil)
	ctx := context.Background()

	if err := c.Do(ctx, http.MethodGet, "/x", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	api.revokeAll()
	if err := c.Do(ctx, http.MethodGet, "/x", nil, nil, nil); err != nil {
		t.Fatalf("after revocation: %v", err)
	}
	if n := api.exchanges.Load(); n != 2 {
		t.Fatalf("%d sign-ins, want 2", n)
	}
}

// A 401 on a token minted for this very call is not retried: signing in again
// would get the same answer, and a loop would hide it.
func TestFreshTokenRefusedIsNotRetried(t *testing.T) {
	key := genKey(t)
	api, srv := newFakeAPI(t, key)
	api.handler = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"insufficient scope"}`)
	}
	c := newTestClient(t, srv, key, nil)
	err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 401 {
		t.Fatalf("got %v, want a 401", err)
	}
	if n := api.exchanges.Load(); n != 1 {
		t.Fatalf("%d sign-ins, want 1", n)
	}
}

// Separate processes share a token through a FileTokenStore: a second client
// on the same store does not sign in again.
func TestFileStoreSharesTheTokenAcrossClients(t *testing.T) {
	key := genKey(t)
	api, srv := newFakeAPI(t, key)
	api.handler = func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) }
	dir := t.TempDir()
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		c := newTestClient(t, srv, key, &FileTokenStore{Dir: dir})
		if err := c.Do(ctx, http.MethodGet, "/x", nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := api.exchanges.Load(); n != 1 {
		t.Fatalf("%d sign-ins across two clients, want 1", n)
	}
}

func TestExpiredCachedTokenIsNotUsed(t *testing.T) {
	key := genKey(t)
	api, srv := newFakeAPI(t, key)
	api.handler = func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) }
	store := &MemoryTokenStore{}
	c := newTestClient(t, srv, key, store)
	// Within the expiry margin counts as expired.
	store.Save(&Token{AccessToken: "old", ExpiresAt: time.Now().Add(30 * time.Second),
		BaseURL: c.BaseURL(), Fingerprint: c.Fingerprint()})

	tok, err := c.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken == "old" {
		t.Fatal("used a token about to expire")
	}
}

func TestAPIErrorKeepsTheServersWords(t *testing.T) {
	key := genKey(t)
	api, srv := newFakeAPI(t, key)
	c := newTestClient(t, srv, key, nil)
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		status int
		body   string
		check  func(*APIError) bool
		substr string
	}{
		{"json", 409, `{"error":"gpu_unavailable","message":"No machine matches type H100."}`,
			func(e *APIError) bool {
				return e.ErrorCode == "gpu_unavailable" && e.Message == "No machine matches type H100."
			},
			"HTTP 409: gpu_unavailable: No machine matches type H100."},
		{"html", 502, "<html><body>\n<h1>502 Bad Gateway</h1>\n</body></html>",
			func(e *APIError) bool { return e.ErrorCode == "" && strings.Contains(e.Body, "502 Bad Gateway") },
			"502 Bad Gateway"},
		{"empty", 503, "",
			func(e *APIError) bool { return e.Body == "" },
			"HTTP 503 with an empty body"},
		{"validation", 400, `{"error":"Validation failed","fields":{"name":"required"}}`,
			func(e *APIError) bool { return e.Fields["name"] == "required" },
			"Validation failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api.handler = func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}
			err := c.Do(ctx, http.MethodPost, "/api/compute/v1/sessions", nil, map[string]string{"a": "b"}, nil)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status || !tc.check(apiErr) {
				t.Fatalf("got %#v", err)
			}
			if !strings.Contains(err.Error(), tc.substr) || !strings.HasPrefix(err.Error(), "POST /api/compute/v1/sessions") {
				t.Fatalf("message %q lacks %q", err.Error(), tc.substr)
			}
		})
	}
}

func TestUnreachableHostIsNamed(t *testing.T) {
	signer, _ := ssh.NewSignerFromKey(genKey(t))
	c, err := New(Options{Host: "http://127.0.0.1:1", Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not reach http://127.0.0.1:1") {
		t.Fatalf("got %v", err)
	}
}

func TestNewRequiresHostAndKey(t *testing.T) {
	if _, err := New(Options{KeyPath: "/nonexistent"}); !errors.Is(err, ErrNoHost) {
		t.Errorf("no host: %v", err)
	}
	// Without a key the client is built, and only a call that needs a token fails.
	c, err := New(Options{Host: "platform"})
	if err != nil {
		t.Fatalf("no key: %v", err)
	}
	if _, err := c.Token(context.Background()); !errors.Is(err, ErrNoKey) {
		t.Errorf("token without a key: %v", err)
	}
	if err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil, nil); !errors.Is(err, ErrNoKey) {
		t.Errorf("Do without a key: %v", err)
	}
}

// The public endpoints are called without signing in, and work without a key.
func TestPublicEndpointsNeedNoKey(t *testing.T) {
	var auth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		if r.URL.Path == "/api/v1/auth/token" {
			t.Error("signed in for a public endpoint")
		}
		io.WriteString(w, `{"gpu_models":[]}`)
	}))
	defer srv.Close()
	c, err := New(Options{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.GPUs().Availability(ctx, AvailabilityOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GPUs().Catalog(ctx); err != nil {
		t.Fatal(err)
	}
	if a, _ := auth.Load().(string); a != "" {
		t.Fatalf("sent Authorization %q", a)
	}
}
