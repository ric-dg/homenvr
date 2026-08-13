package main

import (
	"flag"
	"fmt"
	"os"
)

// version is the v2 development version. Real releases follow v0.1.x from v1.
const version = "0.2.0-dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("homenvrd %s\n", version)
		return
	}

	fmt.Fprintln(os.Stderr, "homenvrd: scaffold only, not yet implemented")
	os.Exit(1)
}
