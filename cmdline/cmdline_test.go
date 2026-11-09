package cmdline

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"strings"
	"testing"
)

// testApp is a two-level program with one global flag, a version, an
// elevated command and a command that records what it ran with.
func testApp(got *[]string) *App {
	var host string
	a := &App{
		Name:   "tool",
		Header: "tool — a test program.\n\n",
		Footer: "\nfooter line\n",
		Marker: "(marked)",
		Globals: func(fs *flag.FlagSet) {
			fs.StringVar(&host, "host", host, "the host")
		},
		Version: func(e *Env) error {
			_, err := e.Stdout.Write([]byte("tool 1.2.3\n"))
			return err
		},
		DescribeError: func(err error) string { return "described: " + err.Error() },
	}
	record := func(name string) func(*Env, *flag.FlagSet, []string) error {
		return func(e *Env, fs *flag.FlagSet, args []string) error {
			*got = append(*got, name+" host="+host+" args="+strings.Join(args, ","))
			return nil
		}
	}
	a.Add("status", &Command{Summary: "Show the status", Run: record("status")})
	a.AddGroup("item", &Group{Summary: "Items", Commands: map[string]*Command{
		"list": {Summary: "List items", Run: record("item list"),
			Flags: func(fs *flag.FlagSet) { fs.Bool("all", false, "every item") }},
		"remove": {Summary: "Remove an item", Usage: "<id>", Elevated: true,
			Run: func(e *Env, fs *flag.FlagSet, args []string) error {
				if len(args) != 1 {
					return UsageError("remove needs one <id>")
				}
				return errors.New("refused")
			}},
	}})
	return a
}

func run(a *App, args ...string) (code int, stdout, stderr string) {
	var o, e bytes.Buffer
	code = a.Run(context.Background(), args, strings.NewReader(""), &o, &e)
	return code, o.String(), e.String()
}

func TestHelpListsWhatExists(t *testing.T) {
	var got []string
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}} {
		code, out, _ := run(testApp(&got), args...)
		want := "tool — a test program.\n\nCommands:\n" +
			"  item          Items\n" +
			"  status        Show the status\n" +
			"\nSetup:\n" +
			"  completion    Tab completion for bash, zsh or fish\n" +
			"\nfooter line\n"
		if code != 0 || out != want {
			t.Errorf("%v: exit %d, help\n%s\nwant\n%s", args, code, out, want)
		}
	}
	code, out, _ := run(testApp(&got), "item")
	if code != 0 || !strings.Contains(out, "tool item — Items") || !strings.Contains(out, "remove           Remove an item (marked)") {
		t.Errorf("group help: exit %d\n%s", code, out)
	}
}

func TestExitCodesSayWhatHappened(t *testing.T) {
	var got []string
	cases := []struct {
		args []string
		code int
		err  string
	}{
		{[]string{"nosuch"}, 2, `tool: unknown command "nosuch"`},
		{[]string{"item", "nosuch"}, 2, `tool: unknown command "item nosuch"`},
		{[]string{"--bogus"}, 2, "tool: flag provided but not defined: -bogus"},
		{[]string{"item", "list", "--bogus"}, 2, "flag provided but not defined: -bogus"},
		{[]string{"item", "remove"}, 2, "tool item remove: remove needs one <id>"},
		{[]string{"item", "remove", "x"}, 1, "tool: described: refused"},
		{[]string{"item", "list", "--help"}, 0, "Usage: tool item list [flags]"},
	}
	for _, c := range cases {
		code, _, errOut := run(testApp(&got), c.args...)
		if code != c.code || !strings.Contains(errOut, c.err) {
			t.Errorf("%v: exit %d, stderr %q; want exit %d containing %q", c.args, code, errOut, c.code, c.err)
		}
	}
}

// A global flag works before the command and after it, and flags may follow
// the positional arguments.
func TestGlobalsAndFlagsGoAnywhere(t *testing.T) {
	for _, args := range [][]string{
		{"--host", "h1", "item", "list", "a", "b"},
		{"item", "list", "a", "--host", "h1", "b"},
		{"item", "list", "a", "b", "--host=h1", "--all"},
	} {
		var got []string
		if code, _, errOut := run(testApp(&got), args...); code != 0 {
			t.Fatalf("%v: exit %d %s", args, code, errOut)
		}
		if len(got) != 1 || got[0] != "item list host=h1 args=a,b" {
			t.Errorf("%v ran %q", args, got)
		}
	}
	// "--" ends the flags: what follows is an argument even if it looks like one.
	var got []string
	run(testApp(&got), "item", "list", "--", "--host", "x")
	if len(got) != 1 || got[0] != "item list host= args=--host,x" {
		t.Errorf("after --: ran %q", got)
	}
}

func TestVersionIsOptional(t *testing.T) {
	var got []string
	if code, out, _ := run(testApp(&got), "--version"); code != 0 || out != "tool 1.2.3\n" {
		t.Errorf("--version: exit %d %q", code, out)
	}
	a := testApp(&got)
	a.Version = nil
	if code, _, _ := run(a, "--version"); code != 2 {
		t.Errorf("--version with no Version: exit %d, want 2 (no such flag)", code)
	}
}

func TestTheNameColumnFitsTheLongestName(t *testing.T) {
	var got []string
	a := testApp(&got)
	a.Add("a-very-long-command", &Command{Summary: "Long", Run: func(*Env, *flag.FlagSet, []string) error { return nil }})
	_, out, _ := run(a)
	if !strings.Contains(out, "  a-very-long-command Long\n") || !strings.Contains(out, "  status              Show") {
		t.Errorf("columns:\n%s", out)
	}
}

func TestPrintJSONDoesNotEscapeHTML(t *testing.T) {
	var b bytes.Buffer
	if err := (&Env{Stdout: &b}).PrintJSON(map[string]string{"a": "<b>&"}); err != nil {
		t.Fatal(err)
	}
	if b.String() != "{\n  \"a\": \"<b>&\"\n}\n" {
		t.Errorf("%q", b.String())
	}
}
