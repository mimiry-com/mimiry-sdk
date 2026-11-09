// Package cli is the human face of the mimiry binary: flags, tables, prompts.
// It decides nothing about the API — every call goes through the library —
// so what it adds is presentation only.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"golang.org/x/term"

	"github.com/mimiry-com/mimiry-sdk/cmdline"
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

// usage is a mistake in how a command was called (cmdline prints it with the
// command's help and exits 2).
type usage = cmdline.UsageError

// Run executes the CLI and returns the process exit code. The framework is
// package cmdline (RC-446); what is mimiry's own is here.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var g env
	app := &cmdline.App{
		Name:   "mimiry",
		Header: topHeader,
		Footer: topFooter,
		Marker: apispec.Marker,
		Globals: func(fs *flag.FlagSet) {
			fs.StringVar(&g.host, "host", g.host, "host: a subdomain, hostname or URL")
			fs.StringVar(&g.key, "key", g.key, "SSH key (private key, or its .pub when in ssh-agent)")
			fs.BoolVar(&g.json, "json", g.json, "print the API's JSON instead of a table")
		},
		Version:       func(ce *cmdline.Env) error { return printVersion(bind(&g, ce)) },
		DescribeError: describeError,
	}
	adapt := func(c *command) *cmdline.Command {
		return &cmdline.Command{
			Summary:  c.summary,
			Usage:    c.usage,
			Help:     c.help,
			Flags:    c.flags,
			Elevated: c.elevated,
			Run: func(ce *cmdline.Env, fs *flag.FlagSet, args []string) error {
				return c.run(bind(&g, ce), fs, args)
			},
		}
	}
	for n, c := range topLevel {
		app.Add(n, adapt(c))
	}
	for n, grp := range groups {
		cmds := make(map[string]*cmdline.Command, len(grp.cmds))
		for cn, c := range grp.cmds {
			cmds[cn] = adapt(c)
		}
		app.AddGroup(n, &cmdline.Group{Summary: grp.summary, Commands: cmds})
	}
	return app.Run(ctx, args, stdin, stdout, stderr)
}

// bind is the env a command runs with: the framework's streams and context,
// and the global flags as parsed.
func bind(g *env, ce *cmdline.Env) *env {
	return &env{ctx: ce.Ctx, stdout: ce.Stdout, stderr: ce.Stderr, host: g.host, key: g.key, json: g.json}
}

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
	return (&cmdline.Env{Stdout: e.stdout}).PrintJSON(v)
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

const topHeader = `mimiry — the Mimiry command line.

Usage: mimiry [--host <host>] [--key <key>] [--json] <command> [args]
       mimiry --version

`

const topFooter = `
The host and key are taken from --host/--key, then MIMIRY_HOST/MIMIRY_SSH_KEY,
then what 'mimiry auth' saved. A host is a subdomain (platform), a hostname or a URL.
'mimiry <command> --help' describes a command.
`
