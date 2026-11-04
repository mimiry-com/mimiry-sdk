package mimiry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Token is an access token and what it was issued for.
type Token struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
	UserID      string    `json:"user_id"`
	Email       string    `json:"email,omitempty"`
	// BaseURL and Fingerprint are the cache key. A token is never reused for
	// another host or another key.
	BaseURL     string `json:"base_url"`
	Fingerprint string `json:"fingerprint"`
}

// expiryMargin is how long before its stated expiry a token stops being used,
// so a request is not sent with a token that expires in flight.
const expiryMargin = 60 * time.Second

func (t *Token) usable(now time.Time) bool {
	return t != nil && t.AccessToken != "" && now.Add(expiryMargin).Before(t.ExpiresAt)
}

// TokenStore keeps tokens between uses. Load returns (nil, nil) when it holds
// no token for the pair.
type TokenStore interface {
	Load(baseURL, fingerprint string) (*Token, error)
	Save(t *Token) error
	Delete(baseURL, fingerprint string) error
}

// MemoryTokenStore keeps tokens for the life of the process. It is the default.
type MemoryTokenStore struct {
	mu     sync.Mutex
	tokens map[string]*Token
}

func (m *MemoryTokenStore) Load(baseURL, fingerprint string) (*Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tokens[tokenKey(baseURL, fingerprint)]
	if t == nil {
		return nil, nil
	}
	c := *t
	return &c, nil
}

func (m *MemoryTokenStore) Save(t *Token) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tokens == nil {
		m.tokens = map[string]*Token{}
	}
	c := *t
	m.tokens[tokenKey(t.BaseURL, t.Fingerprint)] = &c
	return nil
}

func (m *MemoryTokenStore) Delete(baseURL, fingerprint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tokens, tokenKey(baseURL, fingerprint))
	return nil
}

// FileTokenStore keeps one file per host and key in a directory, readable only
// by the user. It is what lets separate processes — every CLI invocation, every
// call from the Python package — share one token instead of signing in each time.
//
// One file per pair rather than one file of all tokens: concurrent processes
// then never read-modify-write the same file, so none can drop another's token.
// Each write is a rename of a complete file, so a reader never sees half of one.
// Two processes that both find no token both sign in; the later write wins and
// both tokens stay valid, which costs one extra exchange and nothing else.
type FileTokenStore struct {
	Dir string
}

func (s *FileTokenStore) path(baseURL, fingerprint string) string {
	return filepath.Join(s.Dir, tokenKey(baseURL, fingerprint)+".json")
}

func (s *FileTokenStore) Load(baseURL, fingerprint string) (*Token, error) {
	data, err := os.ReadFile(s.path(baseURL, fingerprint))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mimiry: read token cache: %w", err)
	}
	var t Token
	if err := json.Unmarshal(data, &t); err != nil {
		// A damaged cache file is not worth failing a call over: it is
		// treated as empty and overwritten by the next sign-in.
		return nil, nil
	}
	// The file name is a hash; the contents must agree with what was asked.
	if t.BaseURL != baseURL || t.Fingerprint != fingerprint {
		return nil, nil
	}
	return &t, nil
}

func (s *FileTokenStore) Save(t *Token) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("mimiry: create token cache: %w", err)
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".token-*")
	if err != nil {
		return fmt.Errorf("mimiry: write token cache: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	// CreateTemp creates the file 0600, so the token is never readable by others.
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("mimiry: write token cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("mimiry: write token cache: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path(t.BaseURL, t.Fingerprint)); err != nil {
		return fmt.Errorf("mimiry: write token cache: %w", err)
	}
	return nil
}

func (s *FileTokenStore) Delete(baseURL, fingerprint string) error {
	err := os.Remove(s.path(baseURL, fingerprint))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("mimiry: delete cached token: %w", err)
	}
	return nil
}

func tokenKey(baseURL, fingerprint string) string {
	sum := sha256.Sum256([]byte(baseURL + "\n" + fingerprint))
	return hex.EncodeToString(sum[:16])
}
