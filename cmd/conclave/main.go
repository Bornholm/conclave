// Command conclave reviews a pull request with several local agents.
package main

import (
	"os"

	"github.com/bornholm/conclave/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
