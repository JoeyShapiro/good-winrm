package main

import (
	"flag"
	"fmt"
	"os"
)

const usage = `usage: rem <target> [--cred NAME] [--port N] [--command CMD]
       rem winrm [user@]host [--cred NAME] [--port N] [--command CMD]
       rem ssh [user@]host [--cred NAME] [--port N] [--command CMD]
       rem serial device [--baud RATE]
       rem add target <name> <winrm|ssh> <address> [--port N]
       rem add cred <name> <username>
       rem remove <target|cred> <name>
       rem list
`

// Words that can't be target or cred names, since they mean something else first.
var reserved = []string{"add", "remove", "list", "help", "winrm", "ssh", "serial"}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	switch args := os.Args[2:]; os.Args[1] {
	case "add":
		return runAdd(args)
	case "remove":
		return runRemove(args)
	case "list":
		return runList(args)
	case "help", "-h", "-help", "--help":
		fmt.Print(usage)
		return nil
	default:
		return runConnect(os.Args[1:])
	}
}

// newFlagSet makes a flag set that prints the usage on errors.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
		fs.PrintDefaults()
	}
	return fs
}

// parseArgs parses flags anywhere among the positional arguments, which
// flag doesn't do by itself since it stops at the first positional one.
func parseArgs(fs *flag.FlagSet, args []string) []string {
	var positional []string
	for {
		fs.Parse(args)
		if fs.NArg() == 0 {
			return positional
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// exitUsage prints the usage and exits like flag does on a bad argument.
func exitUsage(fs *flag.FlagSet) {
	fs.Usage()
	os.Exit(2)
}
