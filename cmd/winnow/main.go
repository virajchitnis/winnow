// Command winnow is the Winnow email-assistant entrypoint.
//
// With no subcommand it runs the service (scheduler + web dashboard). It also
// provides operational subcommands:
//
//	winnow hashpw    generate a bcrypt hash for APP_PASSWORD_HASH
//	winnow sweep     run the one-time initial inbox sweep
//	winnow version   print version information
package main

import (
	"fmt"
	"os"
)

// Build metadata, injected via -ldflags at release time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "hashpw":
		if err := runHashpw(); err != nil {
			fail(err)
		}
	case "version", "--version", "-v":
		fmt.Printf("winnow %s (commit %s, built %s)\n", version, commit, date)
	case "sweep":
		// Wired in internal/schedule; placeholder until that module lands.
		if err := runService(os.Args[1:]); err != nil {
			fail(err)
		}
	case "", "run", "serve":
		if err := runService(os.Args[1:]); err != nil {
			fail(err)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "winnow:", err)
	os.Exit(1)
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: winnow [command]

commands:
  (none) | run | serve   run the service (scheduler + dashboard)
  sweep                  run the one-time initial inbox sweep
  hashpw                 generate a bcrypt hash for the dashboard password
  version                print version information
`)
}
