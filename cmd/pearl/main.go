package main

import (
	"os"

	"github.com/August-H/pearl-cli/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
