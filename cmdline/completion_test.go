package cmdline

import (
	"strings"
	"testing"
)

func TestCompletionOffersWhatFitsWhereTheCursorIs(t *testing.T) {
	cases := []struct {
		words []string
		want  string
	}{
		{nil, "completion help item status"},
		{[]string{""}, "completion help item status"},
		{[]string{"st"}, "status"},
		{[]string{"item", ""}, "list remove"},
		{[]string{"item", "re"}, "remove"},
		{[]string{"-"}, "--host --version"},
		{[]string{"item", "list", "--"}, "--all --host"},
		{[]string{"item", "list", "--a"}, "--all"},
		// A global flag and its value are skipped on the way to the command.
		{[]string{"--host", "h1", "item", ""}, "list remove"},
		{[]string{"--host=h1", "item", ""}, "list remove"},
		// Typing a flag's value: nothing, so the shell can offer files.
		{[]string{"--host", ""}, ""},
		{[]string{"item", "list", "--host", ""}, ""},
		// After --, only arguments.
		{[]string{"item", "list", "--", ""}, ""},
		{[]string{"completion", ""}, "bash fish zsh"},
		{[]string{"status", ""}, ""},
		{[]string{"nosuch", ""}, ""},
	}
	for _, c := range cases {
		var got []string
		_, out, _ := run(testApp(&got), append([]string{completeCommand}, c.words...)...)
		if g := strings.Join(strings.Fields(out), " "); g != c.want {
			t.Errorf("%q: offered %q, want %q", c.words, g, c.want)
		}
		if len(got) != 0 {
			t.Errorf("%q: completion ran a command: %q", c.words, got)
		}
	}
}

func TestCompletionScriptsAskTheBinary(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		var got []string
		code, out, errOut := run(testApp(&got), "completion", shell)
		if code != 0 || !strings.Contains(out, "tool __complete") {
			t.Errorf("%s: exit %d, script does not call back into the binary:\n%s%s", shell, code, out, errOut)
		}
	}
	var got []string
	if code, _, errOut := run(testApp(&got), "completion", "tcsh"); code != 2 || !strings.Contains(errOut, `no completion for "tcsh"`) {
		t.Errorf("unknown shell: exit %d %s", code, errOut)
	}
	if code, _, _ := run(testApp(&got), "completion"); code != 2 {
		t.Errorf("no shell: exit %d, want 2", code)
	}
}

// A name a shell function cannot hold is made safe: mimiry-adm's function is
// _mimiry_adm_complete.
func TestTheShellFunctionNameIsSafe(t *testing.T) {
	a := &App{Name: "mimiry-adm"}
	if a.fn() != "_mimiry_adm_complete" {
		t.Errorf("fn() = %q", a.fn())
	}
}
