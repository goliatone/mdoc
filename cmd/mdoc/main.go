package main

import (
	"os"

	mdoccli "github.com/goliatone/mdoc/internal/cli"
)

func main() {
	if err := mdoccli.Execute(os.Args[1:], os.Stdout, os.Stderr, nil); err != nil {
		mdoccli.WriteError(os.Stderr, err, mdoccli.WantsJSON(os.Args[1:]))
		os.Exit(mdoccli.ExitCode(err))
	}
}
