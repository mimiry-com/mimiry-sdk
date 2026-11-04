package mimiry

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFileTokenStoreRoundTripAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tokens")
	s := &FileTokenStore{Dir: dir}
	want := &Token{AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour).Round(0).UTC(),
		UserID: "u", BaseURL: "https://platform.mimiry.com", Fingerprint: "SHA256:x"}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(want.BaseURL, want.Fingerprint)
	if err != nil || got == nil || got.AccessToken != "a" || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("Load = %+v, %v", got, err)
	}

	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("directory mode %v, %v; want 0700", fi.Mode().Perm(), err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("%d files, want 1 (no temp file left behind)", len(entries))
	}
	if fi, _ := entries[0].Info(); fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode %v, want 0600", fi.Mode().Perm())
	}

	// Another host or key never gets this token.
	for _, k := range [][2]string{{"https://api.example.com", "SHA256:x"}, {want.BaseURL, "SHA256:y"}} {
		if got, _ := s.Load(k[0], k[1]); got != nil {
			t.Errorf("Load(%s, %s) returned another pair's token", k[0], k[1])
		}
	}

	if err := s.Delete(want.BaseURL, want.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Load(want.BaseURL, want.Fingerprint); got != nil {
		t.Fatal("token survived Delete")
	}
	if err := s.Delete(want.BaseURL, want.Fingerprint); err != nil {
		t.Fatalf("deleting an absent token: %v", err)
	}
}

// Concurrent writers for different pairs never lose each other's token.
func TestFileTokenStoreConcurrentWriters(t *testing.T) {
	s := &FileTokenStore{Dir: t.TempDir()}
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.Save(&Token{AccessToken: fmt.Sprint(i), ExpiresAt: time.Now().Add(time.Hour),
				BaseURL: "https://h", Fingerprint: fmt.Sprintf("SHA256:%d", i)})
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		got, err := s.Load("https://h", fmt.Sprintf("SHA256:%d", i))
		if err != nil || got == nil || got.AccessToken != fmt.Sprint(i) {
			t.Fatalf("pair %d: %+v, %v", i, got, err)
		}
	}
}

// A damaged file reads as no token, so the next sign-in replaces it.
func TestFileTokenStoreDamagedFile(t *testing.T) {
	s := &FileTokenStore{Dir: t.TempDir()}
	if err := os.WriteFile(s.path("https://h", "SHA256:x"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load("https://h", "SHA256:x"); got != nil || err != nil {
		t.Fatalf("got %+v, %v; want nothing", got, err)
	}
}

func TestDefaultTokenDirHonoursMimiryHome(t *testing.T) {
	t.Setenv("MIMIRY_HOME", "/tmp/mh")
	if d, err := DefaultTokenDir(); err != nil || d != "/tmp/mh/tokens" {
		t.Fatalf("got %q, %v", d, err)
	}
}

// The file name is only a hash. A file that holds another pair's token — a
// copy, a hash collision — is not served for this one.
func TestFileTokenStoreChecksTheFileIsForThisPair(t *testing.T) {
	s := &FileTokenStore{Dir: t.TempDir()}
	other := &Token{AccessToken: "other", ExpiresAt: time.Now().Add(time.Hour),
		BaseURL: "https://api.example.com", Fingerprint: "SHA256:x"}
	if err := s.Save(other); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.path(other.BaseURL, other.Fingerprint), s.path("https://platform.mimiry.com", "SHA256:x")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Load("https://platform.mimiry.com", "SHA256:x"); got != nil {
		t.Fatalf("served %s's token for platform", got.BaseURL)
	}
}
