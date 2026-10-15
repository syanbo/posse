package main

import (
	"os"

	"hive/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:])) }
