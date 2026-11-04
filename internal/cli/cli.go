// Package cli is the human face of the mimiry binary: flags, tables, prompts.
// It decides nothing about the API — every call goes through the library —
// so what it adds is presentation only.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/term"

	"github.com/mimiry-com/mimiry-sdk/internal/apispec"
	"github.com/mimiry-com/mimiry-sdk/internal/rpc"
	"github.com/mimiry-com/mimiry-sdk/mimiry"
)

// env is what a command runs with.
type env struct {
	ctx    context.Context
	stdout io.Writer
	stderr io.Writer
	// host and key are the global --host / --key values, empty when not given.
	host, key string
	json      bool
}

// command is one leaf of the command tree.
type command struct {
	summary string
	usage   string // arguments after the command name
	help    string // more text, printed after the flags
	run     func(e *env, fs *flag.FlagSet, args []string) error
	flags   func(fs *flag.FlagSet)
	// elevated marks a command that needs elevated rights. Its summary is
	// shown with the marker; a test holds the mark to the published documents.
	// A flag that needs them carries the marker in its own usage instead.
	elevated bool
}

// title is the summary as shown, with the elevated-rights marker if it has one.
func (c *command) title() string {
	if c.elevated {
		return c.summary + " " + apispec.Marker
	}
	return c.summary
}

// group is a set of subcommands under one name.
type group struct {
	summary string
	cmds    map[string]*command
}

var groups = map[string]*group{}
var topLevel = map[string]*command{}

// Run executes the CLI and returns the process exit code.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	e := &env{ctx: ctx, stdout: stdout, stderr: stderr}
	global := flag.NewFlagSet("mimiry", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	global.StringVar(&e.host, "host", "", "")
	global.StringVar(&e.key, "key", "", "")
	global.BoolVar(&e.json, "json", false, "")
	showVersion := global.Bool("version", false, "")
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			topHelp(stdout)
			return 0
		}
		fmt.Fprintf(stderr, "mimiry: %v\n\n", err)
		topHelp(stderr)
		return 2
	}
	if *showVersion {
		if err := printVersion(e); err != nil {
			fmt.Fprintf(stderr, "mimiry: %v\n", err)
			return 1
		}
		return 0
	}
	rest := global.Args()
	if len(rest) == 0 || rest[0] == "help" {
		topHelp(stdout)
		return 0
	}

	name := rest[0]
	var cmd *command
	full := name
	if g, ok := groups[name]; ok {
		if len(rest) < 2 || rest[1] == "help" || rest[1] == "-h" || rest[1] == "--help" {
			groupHelp(stdout, name, g)
			return 0
		}
		cmd = g.cmds[rest[1]]
		full = name + " " + rest[1]
		rest = rest[2:]
		if cmd == nil {
			fmt.Fprintf(stderr, "mimiry: unknown command %q\n\n", full)
			groupHelp(stderr, name, g)
			return 2
		}
	} else if c, ok := topLevel[name]; ok {
		cmd = c
		rest = rest[1:]
	} else {
		fmt.Fprintf(stderr, "mimiry: unknown command %q\n\n", name)
		topHelp(stderr)
		return 2
	}

	fs := flag.NewFlagSet("mimiry "+full, flag.ContinueOnError)
	fs.SetOutput(stderr)
	// Global flags are accepted after the command too: `mimiry session list --json`.
	fs.StringVar(&e.host, "host", e.host, "host: a subdomain, hostname or URL")
	fs.StringVar(&e.key, "key", e.key, "SSH key (private key, or its .pub when in ssh-agent)")
	fs.BoolVar(&e.json, "json", e.json, "print the API's JSON instead of a table")
	if cmd.flags != nil {
		cmd.flags(fs)
	}
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: mimiry %s [flags] %s\n\n%s\n\nFlags:\n", full, cmd.usage, cmd.title())
		fs.PrintDefaults()
		if cmd.help != "" {
			fmt.Fprintln(fs.Output(), cmd.help)
		}
	}
	if err := fs.Parse(interleave(fs, rest)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if err := cmd.run(e, fs, fs.Args()); err != nil {
		var u usage
		if errors.As(err, &u) {
			fmt.Fprintf(stderr, "mimiry %s: %s\n", full, u)
			fs.Usage()
			return 2
		}
		fmt.Fprintf(stderr, "mimiry: %s\n", describeError(err))
		return 1
	}
	return 0
}

// interleave moves flags after positional arguments to the front, so
// `mimiry session status <id> --json` works as people type it.
func interleave(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
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

type usage string

func (u usage) Error() string { return string(u) }

// client builds the library client, prompting for a passphrase when the key
// needs one and there is a terminal to ask on.
func (e *env) client() (*mimiry.Client, error) {
	o, err := mimiry.ResolveOptions(e.host, e.key)
	if err != nil {
		return nil, err
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		o.Passphrase = func() ([]byte, error) {
			fmt.Fprintf(e.stderr, "Passphrase for %s: ", o.KeyPath)
			p, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(e.stderr)
			return p, err
		}
	}
	o.UserAgent = "mimiry-cli"
	return mimiry.New(o)
}

// printVersion says which build this is. It needs no host or key, so it
// works before 'mimiry auth' and when the platform cannot be reached.
func printVersion(e *env) error {
	goBuild := runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH
	if e.json {
		return e.printJSON(map[string]any{
			"version":  mimiry.Version,
			"protocol": rpc.Protocol,
			"go":       goBuild,
		})
	}
	_, err := fmt.Fprintf(e.stdout, "mimiry %s (rpc protocol %d, %s)\n", mimiry.Version, rpc.Protocol, goBuild)
	return err
}

func (e *env) printJSON(v any) error {
	enc := json.NewEncoder(e.stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// describeError says what went wrong and, where the cause is a known one,
// what to do about it.
func describeError(err error) string {
	switch {
	case errors.Is(err, mimiry.ErrNoHost):
		return "no host is set. Pass --host <subdomain|hostname|URL> (e.g. --host platform), set MIMIRY_HOST, or run `mimiry auth --host <host> --key <key>` once to remember it."
	case errors.Is(err, mimiry.ErrNoKey):
		return "no SSH key is set for this host. Pass --key <path>, set MIMIRY_SSH_KEY, or run `mimiry auth --key <path>` once to remember it."
	case errors.Is(err, mimiry.ErrKeyLocked):
		return "the SSH key is passphrase-protected and there is no terminal to ask on. Load it into ssh-agent (ssh-add <key>)."
	}
	var f *mimiry.SessionFailedError
	if errors.As(err, &f) {
		return err.Error()
	}
	return strings.TrimPrefix(err.Error(), "mimiry: ")
}

func topHelp(w io.Writer) {
	fmt.Fprint(w, `mimiry — the Mimiry command line.

Usage: mimiry [--host <host>] [--key <key>] [--json] <command> [args]
       mimiry --version

`)
	names := make([]string, 0, len(groups)+len(topLevel))
	for n := range groups {
		names = append(names, n)
	}
	for n := range topLevel {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintln(w, "Commands:")
	for _, n := range names {
		if g, ok := groups[n]; ok {
			fmt.Fprintf(w, "  %-13s %s\n", n, g.summary)
		} else {
			fmt.Fprintf(w, "  %-13s %s\n", n, topLevel[n].title())
		}
	}
	fmt.Fprint(w, `
The host and key are taken from --host/--key, then MIMIRY_HOST/MIMIRY_SSH_KEY,
then what 'mimiry auth' saved. A host is a subdomain (platform), a hostname or a URL.
'mimiry <command> --help' describes a command.
`)
}

func groupHelp(w io.Writer, name string, g *group) {
	fmt.Fprintf(w, "mimiry %s — %s\n\nCommands:\n", name, g.summary)
	names := make([]string, 0, len(g.cmds))
	for n := range g.cmds {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "  %-16s %s\n", n, g.cmds[n].title())
	}
}
