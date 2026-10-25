package main

import (
	"os"

	"posse/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:])) }
