package cmdline_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mimiry-com/mimiry-sdk/cmdline"
)

// A program with one global flag, one command and one group. Written outside
// the package, so it uses only what another program can.
func Example() {
	var host string
	app := &cmdline.App{
		Name:   "hello",
		Header: "hello — greets people.\n\n",
		Globals: func(fs *flag.FlagSet) {
			fs.StringVar(&host, "host", host, "where to greet from")
		},
	}
	app.Add("greet", &cmdline.Command{
		Summary: "Greet someone",
		Usage:   "<name>",
		Flags:   func(fs *flag.FlagSet) { fs.Bool("loud", false, "shout") },
		Run: func(e *cmdline.Env, fs *flag.FlagSet, args []string) error {
			if len(args) != 1 {
				return cmdline.UsageError("greet needs one <name>")
			}
			msg := "hello, " + args[0] + " from " + host
			if fs.Lookup("loud").Value.String() == "true" {
				msg = strings.ToUpper(msg)
			}
			_, err := fmt.Fprintln(e.Stdout, msg)
			return err
		},
	})
	app.AddGroup("team", &cmdline.Group{Summary: "Teams", Commands: map[string]*cmdline.Command{
		"list": {Summary: "List teams", Run: func(e *cmdline.Env, _ *flag.FlagSet, _ []string) error {
			return e.PrintJSON([]string{"red", "blue"})
		}},
	}})

	ctx := context.Background()
	app.Run(ctx, []string{"greet", "ada", "--host", "earth", "--loud"}, os.Stdin, os.Stdout, os.Stderr)
	app.Run(ctx, []string{"team", "list"}, os.Stdin, os.Stdout, os.Stderr)
	app.Run(ctx, []string{"__complete", "te"}, os.Stdin, os.Stdout, os.Stderr)
	// Output:
	// HELLO, ADA FROM EARTH
	// [
	//   "red",
	//   "blue"
	// ]
	// team
}
