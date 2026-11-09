package cmdline

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Tab completion. Every program gets it:
//
//	<name> completion bash|zsh|fish   prints a script to load in that shell
//	<name> __complete <words...>      answers one completion, one candidate a line
//
// The script holds no list of commands. It asks the binary on every TAB, so
// what completes is always what the installed binary has: upgrading it
// updates completion, and nothing needs reinstalling.

// completeCommand is the hidden command the shell scripts call.
const completeCommand = "__complete"

// shells are the shells a completion script is written for.
var shells = []string{"bash", "fish", "zsh"}

func (a *App) completionHelp() string {
	return fmt.Sprintf(`Load it in your shell's start-up file:
  bash   source <(%[1]s completion bash)            in ~/.bashrc
  zsh    source <(%[1]s completion zsh)             in ~/.zshrc, after compinit
  fish   %[1]s completion fish > ~/.config/fish/completions/%[1]s.fish

Homebrew installs it for you. The script asks %[1]s itself what to offer, so
it stays current when %[1]s is upgraded.`, a.Name)
}

// completionCmd prints the script for one shell.
func (a *App) completionCmd() *Command {
	return &Command{
		Summary: "Tab completion for bash, zsh or fish",
		Usage:   "<bash|zsh|fish>",
		Help:    "\n" + a.completionHelp(),
		Run: func(e *Env, fs *flag.FlagSet, args []string) error {
			if len(args) != 1 {
				return UsageError("name one shell: bash, zsh or fish")
			}
			script, ok := a.script(args[0])
			if !ok {
				return UsageError(fmt.Sprintf("no completion for %q: bash, zsh or fish", args[0]))
			}
			_, err := io.WriteString(e.Stdout, script)
			return err
		},
	}
}

// fn is the name of the shell function: the program's name with anything a
// shell function name cannot hold replaced.
func (a *App) fn() string {
	return "_" + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, a.Name) + "_complete"
}

func (a *App) script(shell string) (string, bool) {
	n, f := a.Name, a.fn()
	switch shell {
	case "bash":
		return fmt.Sprintf(`# bash completion for %[1]s. Load it with: source <(%[1]s completion bash)
%[2]s() {
  local IFS=$'\n'
  COMPREPLY=($(%[1]s %[3]s "${COMP_WORDS[@]:1:COMP_CWORD}" 2>/dev/null))
}
complete -o default -F %[2]s %[1]s
`, n, f, completeCommand), true
	case "zsh":
		return fmt.Sprintf(`#compdef %[1]s
# zsh completion for %[1]s. Load it with: source <(%[1]s completion zsh)
%[2]s() {
  local -a c
  c=("${(@f)$(%[1]s %[3]s "${(@)words[2,CURRENT]}" 2>/dev/null)}")
  c=(${c:#})
  if (( ${#c} )); then compadd -a c; else _files; fi
}
compdef %[2]s %[1]s
`, n, f, completeCommand), true
	case "fish":
		return fmt.Sprintf(`# fish completion for %[1]s. Save it as ~/.config/fish/completions/%[1]s.fish
complete -c %[1]s -f -a '(%[1]s %[2]s (commandline -opc)[2..-1] (commandline -ct))'
`, n, completeCommand), true
	}
	return "", false
}

// complete writes the candidates for the last of words, the word being
// completed (empty when the cursor is after a space). The words before it say
// where on the command line the cursor is.
func (a *App) complete(w io.Writer, words []string) {
	if len(words) == 0 {
		words = []string{""}
	}
	cur, before := words[len(words)-1], words[:len(words)-1]

	// Walk the words before the cursor: global flags and their values are
	// skipped, everything else names the command.
	var path []string
	for i := 0; i < len(before); i++ {
		t := before[i]
		if t == "--" {
			return // after --, only arguments: nothing to offer
		}
		if strings.HasPrefix(t, "-") && t != "-" {
			if strings.Contains(t, "=") || !a.takesValue(path, strings.TrimLeft(t, "-")) {
				continue
			}
			if i+1 == len(before) {
				return // completing a flag's value: let the shell offer files
			}
			i++
			continue
		}
		path = append(path, t)
	}

	var out []string
	switch {
	case strings.HasPrefix(cur, "-"):
		if fs := a.flagSet(path); fs != nil {
			fs.VisitAll(func(f *flag.Flag) { out = append(out, "--"+f.Name) })
		}
	case len(path) == 0:
		for n := range a.top {
			out = append(out, n)
		}
		for n := range a.groups {
			out = append(out, n)
		}
		out = append(out, "completion", "help")
	case len(path) == 1 && path[0] == "completion":
		out = append(out, shells...)
	case len(path) == 1:
		if g, ok := a.groups[path[0]]; ok {
			for n := range g.Commands {
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	for _, c := range out {
		if strings.HasPrefix(c, cur) {
			fmt.Fprintln(w, c)
		}
	}
}

// command finds the command path names, or nil.
func (a *App) command(path []string) *Command {
	switch {
	case len(path) == 1 && path[0] == "completion":
		return a.completionCmd()
	case len(path) == 1:
		return a.top[path[0]]
	case len(path) >= 2:
		if g, ok := a.groups[path[0]]; ok {
			return g.Commands[path[1]]
		}
	}
	return nil
}

// flagSet is the flags accepted where path ends: the globals, plus the
// command's own once path names a command.
func (a *App) flagSet(path []string) *flag.FlagSet {
	fs := flag.NewFlagSet(a.Name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if a.Globals != nil {
		a.Globals(fs)
	}
	if c := a.command(path); c != nil && c.Flags != nil {
		c.Flags(fs)
	}
	if len(path) == 0 && a.Version != nil {
		fs.Bool("version", false, "")
	}
	return fs
}

// takesValue reports whether the flag name, where path ends, needs a value.
func (a *App) takesValue(path []string, name string) bool {
	f := a.flagSet(path).Lookup(name)
	if f == nil {
		return false
	}
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return !ok || !b.IsBoolFlag()
}
