package main

import (
	"os"
	"os/signal"
	"syscall"

	mdoccli "github.com/goliatone/mdoc/internal/cli"
)

func main() {
	// Contributions must report durable recovery status even when the output
	// consumer closes its pipe. Let writes return EPIPE instead of exiting before
	// the CLI can classify the outcome and report it on stderr.
	if len(os.Args) > 1 && os.Args[1] == "contribution" {
		signal.Ignore(syscall.SIGPIPE)
	}
	if err := mdoccli.Execute(os.Args[1:], os.Stdout, os.Stderr, nil); err != nil {
		mdoccli.WriteError(os.Stderr, err, mdoccli.WantsJSON(os.Args[1:]))
		os.Exit(mdoccli.ExitCode(err))
	}
}
