package mimiry

import (
	"os"
	"runtime/debug"
	"strings"
	"testing"
)

func info(main debug.Module, deps ...*debug.Module) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: main, Deps: deps}, true
	}
}

func TestVersionComesFromWhatTheBuildRecorded(t *testing.T) {
	pseudo := "v0.0.0-20261103145553-9b5a64c05641"
	cases := []struct {
		name   string
		linked string
		read   func() (*debug.BuildInfo, bool)
		want   string
	}{
		{"go install of the CLI", "", info(debug.Module{Path: modulePath, Version: pseudo}), pseudo},
		{"a checkout with changes", "", info(debug.Module{Path: modulePath, Version: pseudo + "+dirty"}), pseudo + "+dirty"},
		{"a release build", "v0.1.0", info(debug.Module{Path: modulePath, Version: pseudo}), "v0.1.0"},
		{"a program importing the library", "",
			info(debug.Module{Path: "example.com/app", Version: "v1.2.3"},
				&debug.Module{Path: "golang.org/x/term", Version: "v0.47.0"},
				&debug.Module{Path: modulePath, Version: "v0.1.0"}),
			"v0.1.0"},
		{"a program replacing the library", "",
			info(debug.Module{Path: "example.com/app"},
				&debug.Module{Path: modulePath, Version: "v0.1.0", Replace: &debug.Module{Path: "../sdk"}}),
			devVersion},
		{"no version recorded", "", info(debug.Module{Path: modulePath, Version: "(devel)"}), devVersion},
		{"no build information", "", func() (*debug.BuildInfo, bool) { return nil, false }, devVersion},
		{"the library is not in the binary", "", info(debug.Module{Path: "example.com/app", Version: "v1.2.3"}), devVersion},
	}
	for _, c := range cases {
		if got := buildVersion(c.linked, c.read); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// modulePath is how the library finds itself in the build record. A module
// renamed in go.mod and not here would report devVersion everywhere, silently.
func TestModulePathIsGoMods(t *testing.T) {
	b, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(b), "\n")
	if got := strings.TrimSpace(strings.TrimPrefix(first, "module")); got != modulePath {
		t.Errorf("go.mod declares module %q; version.go looks for %q", got, modulePath)
	}
}
