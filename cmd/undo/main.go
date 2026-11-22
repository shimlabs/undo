package main

import (
	"fmt"
	"os"

	"github.com/shimlabs/undo/internal/cli"
)

func main() {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "undo:", err)
		os.Exit(1)
	}
	os.Exit(cli.Run(os.Args[1:], cli.Env{Dir: dir, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}))
}
