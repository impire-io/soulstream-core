// Command soulstream is a terminal client for a Soulstream persona.
package main

import (
	"os"

	"github.com/impire-io/soulstream-core/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
