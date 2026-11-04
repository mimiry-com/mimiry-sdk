package mimiry

import (
	"errors"
	"testing"
)

func TestResolveBaseURL(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"platform", "https://platform.mimiry.com"},
		{"  platform  ", "https://platform.mimiry.com"},
		{"api.example.com", "https://api.example.com"},
		{"api.example.com.", "https://api.example.com"},
		// A hostname is never refused for an identifier-shaped prefix.
		{"staging-api.example.com", "https://staging-api.example.com"},
		{"https://platform.mimiry.com/", "https://platform.mimiry.com"},
		{"http://localhost:8080", "http://localhost:8080"},
	} {
		got, err := ResolveBaseURL(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ResolveBaseURL(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestResolveBaseURLRefuses(t *testing.T) {
	for _, in := range []string{
		"prod-eu1", // an instance identifier is not a host
		"prod-alpha",
		"dev-local",
		"platform/api",
		"two words",
		"Trunk_",
		"https://",
	} {
		if got, err := ResolveBaseURL(in); err == nil {
			t.Errorf("ResolveBaseURL(%q) = %q, want an error", in, got)
		}
	}
	if _, err := ResolveBaseURL(""); !errors.Is(err, ErrNoHost) {
		t.Errorf("empty host: got %v, want ErrNoHost", err)
	}
}
