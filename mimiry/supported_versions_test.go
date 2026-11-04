package mimiry

// SUPPORTED-VERSIONS is the one place the SDK's minimum language versions are
// decided, with the next step and the date it is due. This test holds go.mod's
// `go` directive to it, and fails once a switch is due, so a minimum cannot
// drift from the file, and a planned switch cannot be quietly missed.
//
// The python row is held to requires-python by the Python package's own test.

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

type supportedVersion struct {
	language, minimum, next string
	switchBy                time.Time
}

func readSupportedVersions(t *testing.T) map[string]supportedVersion {
	t.Helper()
	raw, err := os.ReadFile("../SUPPORTED-VERSIONS")
	if err != nil {
		t.Fatalf("read SUPPORTED-VERSIONS: %v", err)
	}
	rows := map[string]supportedVersion{}
	for n, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 4 {
			t.Fatalf("SUPPORTED-VERSIONS:%d: want `language minimum next switch-by`, got %q", n+1, line)
		}
		by, err := time.Parse("2006-01-02", f[3])
		if err != nil {
			t.Fatalf("SUPPORTED-VERSIONS:%d: switch-by %q is not a date: %v", n+1, f[3], err)
		}
		if _, dup := rows[f[0]]; dup {
			t.Fatalf("SUPPORTED-VERSIONS:%d: %s appears twice", n+1, f[0])
		}
		rows[f[0]] = supportedVersion{f[0], f[1], f[2], by}
	}
	return rows
}

// versionLess compares dotted numeric versions: 1.26.0 < 1.27.0, 3.9 < 3.11.
func versionLess(t *testing.T, a, b string) bool {
	t.Helper()
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		var err error
		if i < len(pa) {
			if x, err = strconv.Atoi(pa[i]); err != nil {
				t.Fatalf("version %q is not numeric", a)
			}
		}
		if i < len(pb) {
			if y, err = strconv.Atoi(pb[i]); err != nil {
				t.Fatalf("version %q is not numeric", b)
			}
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func TestSupportedVersionsAreWellFormed(t *testing.T) {
	rows := readSupportedVersions(t)
	for _, lang := range []string{"golang", "python"} {
		if _, ok := rows[lang]; !ok {
			t.Errorf("SUPPORTED-VERSIONS has no %s row", lang)
		}
	}
	for _, r := range rows {
		if !versionLess(t, r.minimum, r.next) {
			t.Errorf("%s: next %s is not after minimum %s", r.language, r.next, r.minimum)
		}
	}
}

func TestGoDirectiveIsTheSupportedMinimum(t *testing.T) {
	want := readSupportedVersions(t)["golang"].minimum
	mod, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	m := regexp.MustCompile(`(?m)^go (\S+)\s*$`).FindSubmatch(mod)
	if m == nil {
		t.Fatal("go.mod has no go directive")
	}
	if got := string(m[1]); got != want {
		t.Errorf("go.mod says go %s; SUPPORTED-VERSIONS says the minimum is %s. "+
			"Set the directive with `go mod edit -go=%s`, never by a tidy on a newer toolchain", got, want, want)
	}
	if regexp.MustCompile(`(?m)^toolchain `).Match(mod) {
		t.Error("go.mod has a toolchain line; the minimum is the go directive alone")
	}
}

func TestNoVersionSwitchIsOverdue(t *testing.T) {
	today := time.Now().UTC()
	for _, r := range readSupportedVersions(t) {
		if !today.Before(r.switchBy) {
			t.Errorf("%s: the switch to %s was due by %s. Follow SUPPORTED-VERSIONS' header: "+
				"minimum becomes %s, then set the next step and its date",
				r.language, r.next, r.switchBy.Format("2006-01-02"), r.next)
		}
	}
}
