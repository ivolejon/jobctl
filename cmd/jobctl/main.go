package main

import (
	"os"

	"github.com/ivolejon/jobctl/internal/cli"
)

var version, commit string

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr, cli.Build{Version: version, Commit: commit}))
}
