// Package cmdline is the command-line framework every Mimiry Go command is
// built on: a tree of commands one or two words deep, flags from the standard
// library, help that lists what exists, and exit codes that say what happened.
//
// It is deliberately small and has no dependency outside the standard library.
// What a program adds is its own: its name, its global flags, its version line
// and how it words an error. The commands decide everything else.
//
// Exit codes: 0 success or help asked for; 1 the command failed; 2 the command
// line was wrong (unknown command or flag, or a UsageError).
package cmdline

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

// App is one command-line program.
type App struct {
	// Name is the program's name, as typed. It begins every usage line and
	// every error line.
	Name string
	// Header is printed first by the top-level help, verbatim. End it with a
	// blank line.
	Header string
	// Footer is printed after the top-level command list, verbatim.
	Footer string
	// Marker is appended to the summary of a command marked Elevated.
	Marker string
	// Globals registers the program's global flags. It is called twice on
	// every run: once before the command name and once after it, so a global
	// flag may be given in either place. Bind both calls to the same variables,
	// and use each variable's current value as its default, so the second call
	// keeps what the first one parsed.
	Globals func(fs *flag.FlagSet)
	// Version prints the version, for --version. Nil means the program has no
	// --version flag.
	Version func(e *Env) error
	// DescribeError words a failed command's error, printed after "<Name>: ".
	// Nil prints err.Error().
	DescribeError func(err error) string

	top    map[string]*Command
	groups map[string]*Group
}

// Command is one runnable command.
type Command struct {
	// Summary is one line, shown in the command lists and in its own help.
	Summary string
	// Usage names the arguments after the command and its flags.
	Usage string
	// Help is more text, printed after the flags in the command's own help.
	Help string
	// Flags registers the command's own flags.
	Flags func(fs *flag.FlagSet)
	// Run does the work. args are what remains after the flags.
	Run func(e *Env, fs *flag.FlagSet, args []string) error
	// Elevated marks a command that needs more than an ordinary caller's
	// rights. Its summary is shown with the App's Marker.
	Elevated bool
}

// Group is a set of commands under one name: `<name> <group> <command>`.
type Group struct {
	Summary  string
	Commands map[string]*Command
}

// Env is what a command runs with.
type Env struct {
	Ctx    context.Context
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// PrintJSON writes v to Stdout as indented JSON, without HTML escaping.
func (e *Env) PrintJSON(v any) error {
	enc := json.NewEncoder(e.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// UsageError is a mistake in how a command was called. Run prints it with the
// command's help and exits 2.
type UsageError string

func (u UsageError) Error() string { return string(u) }

// Add registers a top-level command.
func (a *App) Add(name string, c *Command) {
	if a.top == nil {
		a.top = map[string]*Command{}
	}
	a.top[name] = c
}

// AddGroup registers a group of commands.
func (a *App) AddGroup(name string, g *Group) {
	if a.groups == nil {
		a.groups = map[string]*Group{}
	}
	a.groups[name] = g
}

// Title is a command's summary as shown: with the marker when it is Elevated.
func (a *App) Title(c *Command) string {
	if c.Elevated && a.Marker != "" {
		return c.Summary + " " + a.Marker
	}
	return c.Summary
}

// Run executes the program with args (without the program name) and returns
// the process exit code.
func (a *App) Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	e := &Env{Ctx: ctx, Stdin: stdin, Stdout: stdout, Stderr: stderr}
	global := flag.NewFlagSet(a.Name, flag.ContinueOnError)
	global.SetOutput(io.Discard)
	if a.Globals != nil {
		a.Globals(global)
	}
	var showVersion *bool
	if a.Version != nil {
		showVersion = global.Bool("version", false, "")
	}
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			a.topHelp(stdout)
			return 0
		}
		fmt.Fprintf(stderr, "%s: %v\n\n", a.Name, err)
		a.topHelp(stderr)
		return 2
	}
	if showVersion != nil && *showVersion {
		if err := a.Version(e); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", a.Name, err)
			return 1
		}
		return 0
	}
	rest := global.Args()
	if len(rest) == 0 || rest[0] == "help" {
		a.topHelp(stdout)
		return 0
	}

	name := rest[0]
	var cmd *Command
	full := name
	if g, ok := a.groups[name]; ok {
		if len(rest) < 2 || rest[1] == "help" || rest[1] == "-h" || rest[1] == "--help" {
			a.groupHelp(stdout, name, g)
			return 0
		}
		cmd = g.Commands[rest[1]]
		full = name + " " + rest[1]
		rest = rest[2:]
		if cmd == nil {
			fmt.Fprintf(stderr, "%s: unknown command %q\n\n", a.Name, full)
			a.groupHelp(stderr, name, g)
			return 2
		}
	} else if c, ok := a.top[name]; ok {
		cmd = c
		rest = rest[1:]
	} else {
		fmt.Fprintf(stderr, "%s: unknown command %q\n\n", a.Name, name)
		a.topHelp(stderr)
		return 2
	}

	fs := flag.NewFlagSet(a.Name+" "+full, flag.ContinueOnError)
	fs.SetOutput(stderr)
	// Global flags are accepted after the command too: `<name> group cmd --json`.
	if a.Globals != nil {
		a.Globals(fs)
	}
	if cmd.Flags != nil {
		cmd.Flags(fs)
	}
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s %s [flags] %s\n\n%s\n\nFlags:\n", a.Name, full, cmd.Usage, a.Title(cmd))
		fs.PrintDefaults()
		if cmd.Help != "" {
			fmt.Fprintln(fs.Output(), cmd.Help)
		}
	}
	if err := fs.Parse(interleave(fs, rest)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if err := cmd.Run(e, fs, fs.Args()); err != nil {
		var u UsageError
		if errors.As(err, &u) {
			fmt.Fprintf(stderr, "%s %s: %s\n", a.Name, full, u)
			fs.Usage()
			return 2
		}
		msg := err.Error()
		if a.DescribeError != nil {
			msg = a.DescribeError(err)
		}
		fmt.Fprintf(stderr, "%s: %s\n", a.Name, msg)
		return 1
	}
	return 0
}

// interleave moves flags after positional arguments to the front, so
// `<name> session status <id> --json` works as people type it. A "--" ends the
// flags: everything after it is an argument, and the "--" is kept so that the
// flag parser stops there too (until RC-446 it was dropped, and an argument
// after it that looked like a flag was parsed as one).
func interleave(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return append(append(flags, "--"), append(pos, args[i+1:]...)...)
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return append(flags, pos...)
}

func (a *App) topHelp(w io.Writer) {
	fmt.Fprint(w, a.Header)
	names := make([]string, 0, len(a.groups)+len(a.top))
	for n := range a.groups {
		names = append(names, n)
	}
	for n := range a.top {
		names = append(names, n)
	}
	sort.Strings(names)
	width := column(names, 13)
	fmt.Fprintln(w, "Commands:")
	for _, n := range names {
		if g, ok := a.groups[n]; ok {
			fmt.Fprintf(w, "  %-*s %s\n", width, n, g.Summary)
		} else {
			fmt.Fprintf(w, "  %-*s %s\n", width, n, a.Title(a.top[n]))
		}
	}
	fmt.Fprint(w, a.Footer)
}

func (a *App) groupHelp(w io.Writer, name string, g *Group) {
	fmt.Fprintf(w, "%s %s — %s\n\nCommands:\n", a.Name, name, g.Summary)
	names := make([]string, 0, len(g.Commands))
	for n := range g.Commands {
		names = append(names, n)
	}
	sort.Strings(names)
	width := column(names, 16)
	for _, n := range names {
		fmt.Fprintf(w, "  %-*s %s\n", width, n, a.Title(g.Commands[n]))
	}
}

// column is the width of a command-name column: at least min, and as wide as
// the longest name (the format puts a space after the column).
func column(names []string, min int) int {
	w := min
	for _, n := range names {
		if len(n) > w {
			w = len(n)
		}
	}
	return w
}
