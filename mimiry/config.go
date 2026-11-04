package mimiry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Config is what the user chose to remember: a default host, and which key
// they use on each host. `mimiry auth` writes it; every caller — the CLI, the
// Python package, a Go program using ResolveOptions — reads it, so a choice
// made once applies everywhere.
type Config struct {
	// Host is the default host, as the user wrote it ("platform").
	Host string `json:"host,omitempty"`
	// Keys maps a resolved base URL to the SSH key used there.
	Keys map[string]string `json:"keys,omitempty"`
}

// HomeDir is $MIMIRY_HOME, or ~/.mimiry.
func HomeDir() (string, error) {
	if h := os.Getenv("MIMIRY_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("mimiry: locate home directory: %w", err)
	}
	return filepath.Join(home, ".mimiry"), nil
}

// DefaultTokenDir is HomeDir/tokens.
func DefaultTokenDir() (string, error) {
	h, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "tokens"), nil
}

func configPath() (string, error) {
	h, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "config.json"), nil
}

// LoadConfig reads the saved config. A missing file is an empty config; an
// unreadable or damaged one is an error, because guessing past it would
// silently drop the user's choices.
func LoadConfig() (*Config, error) {
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mimiry: read %s: %w", p, err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("mimiry: %s is not valid JSON: %w", p, err)
	}
	return &c, nil
}

// Save writes the config, readable only by the user.
func (c *Config) Save() error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("mimiry: save config: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".config-*")
	if err != nil {
		return fmt.Errorf("mimiry: save config: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("mimiry: save config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("mimiry: save config: %w", err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return fmt.Errorf("mimiry: save config: %w", err)
	}
	return nil
}

// ResolveOptions decides the host and key the way every Mimiry tool does, so
// they cannot disagree. For each, the first that is set wins:
//
//	host: the host argument, MIMIRY_HOST, the saved default
//	key:  the key argument, MIMIRY_SSH_KEY, the key saved for that host
//
// Tokens are cached on disk (DefaultTokenDir), shared between processes.
func ResolveOptions(host, key string) (Options, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return Options{}, err
	}
	o := Options{Host: firstSet(host, os.Getenv("MIMIRY_HOST"), cfg.Host)}
	base, err := ResolveBaseURL(o.Host)
	if err != nil {
		return Options{}, err
	}
	o.KeyPath = firstSet(key, os.Getenv("MIMIRY_SSH_KEY"), cfg.Keys[base])
	dir, err := DefaultTokenDir()
	if err != nil {
		return Options{}, err
	}
	o.TokenStore = &FileTokenStore{Dir: dir}
	return o, nil
}

func firstSet(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
