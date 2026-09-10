// Command cli is a personal toolbox of day-to-day tools.
//
// Each tool lives under its own subcommand; run "cli" with no arguments to see
// what is available.
package main

import (
	"os"

	"github.com/ETLopes/cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
