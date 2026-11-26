package cli

// `session exec`, `session upload` and `session download` (RC-473): the
// library's Dial, for people and scripts. A command runs without a
// terminal; for an interactive shell, `session ssh`.

import (
	"flag"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"github.com/mimiry-com/mimiry-sdk/cmdline"
	"github.com/mimiry-com/mimiry-sdk/mimiry"
)

func init() {
	s := groups["session"].cmds
	s["exec"] = &command{
		summary: "Run a command in a session and return its exit status",
		usage:   "<id> -- <program> [args]",
		help:    sessionExecHelp,
		run:     runSessionExec,
	}
	s["upload"] = &command{
		summary: "Copy a file into a session",
		usage:   "<id> <local file> <path in the session>",
		run:     runSessionUpload,
	}
	s["download"] = &command{
		summary: "Copy a file out of a session",
		usage:   "<id> <path in the session> <local file>",
		help: `
The local file is created readable by you only. An existing file is not
replaced unless --overwrite is given.`,
		flags: func(fs *flag.FlagSet) {
			fs.Bool("overwrite", false, "replace the local file if it exists")
		},
		run: runSessionDownload,
	}
}

const sessionExecHelp = `
Each argument reaches the program as it is, with no shell in between; for a
shell, run one: -- sh -c 'nvidia-smi | head -5'. Input piped to this
command is the program's input. The program's exit status is this command's.

  mimiry session exec <id> -- nvidia-smi
  mimiry session exec <id> -- python3 train.py --epochs 3`

func runSessionExec(e *env, _ *flag.FlagSet, args []string) error {
	if len(args) == 0 || args[0] == "" {
		return usage("expected a session ID")
	}
	id, argv := args[0], args[1:]
	if len(argv) > 0 && argv[0] == "--" {
		argv = argv[1:]
	}
	if len(argv) == 0 {
		return usage("expected a command after the session ID: <id> -- <program> [args]")
	}
	x, err := e.dial(id)
	if err != nil {
		return err
	}
	defer x.Close()
	// A terminal is not passed on: the program would wait for input that
	// nobody knows it wants. Piped input is.
	var stdin io.Reader
	if f, ok := e.stdin.(*os.File); !ok || !term.IsTerminal(int(f.Fd())) {
		stdin = e.stdin
	}
	code, err := x.Exec(e.ctx, argv, mimiry.ExecIO{Stdin: stdin, Stdout: e.stdout, Stderr: e.stderr})
	if err != nil {
		return err
	}
	if code != 0 {
		return cmdline.ExitCode(code)
	}
	return nil
}

func runSessionUpload(e *env, _ *flag.FlagSet, args []string) error {
	if len(args) != 3 {
		return usage("expected <id> <local file> <path in the session>")
	}
	x, err := e.dial(args[0])
	if err != nil {
		return err
	}
	defer x.Close()
	n, err := x.UploadFile(e.ctx, args[1], args[2])
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stderr, "Copied %d bytes to %s\n", n, args[2])
	return nil
}

func runSessionDownload(e *env, fs *flag.FlagSet, args []string) error {
	if len(args) != 3 {
		return usage("expected <id> <path in the session> <local file>")
	}
	x, err := e.dial(args[0])
	if err != nil {
		return err
	}
	defer x.Close()
	n, err := x.DownloadFile(e.ctx, args[1], args[2], boolFlag(fs, "overwrite"))
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stderr, "Copied %d bytes to %s\n", n, args[2])
	return nil
}

// dial opens an SSH connection to a session through the gateway.
func (e *env) dial(id string) (*mimiry.SessionSSH, error) {
	c, err := e.client()
	if err != nil {
		return nil, err
	}
	return c.Sessions().Dial(e.ctx, id, mimiry.DialOptions{})
}
