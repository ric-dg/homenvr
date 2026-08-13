package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/ric-dg/homenvr/internal/config"
)

// version is the v2 development version. Real releases follow v0.1.x from v1.
const version = "0.2.0-dev"

func main() {
	configPath := flag.String("config", "config.jsonc", "path to the JSONC config")
	dump := flag.Bool("dump-config", false, "print the effective config as JSON and exit")
	validate := flag.Bool("validate-config", false, "validate the config and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("homenvrd %s\n", version)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "homenvrd: config: %v\n", err)
		os.Exit(1)
	}

	if *dump {
		b, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(string(b))
		return
	}

	if *validate {
		if err := cfg.Validate(); err != nil {
			fmt.Fprintf(os.Stderr, "homenvrd: invalid config: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("config OK")
		return
	}

	fmt.Fprintln(os.Stderr, "homenvrd: daemon not yet implemented; use -dump-config or -validate-config")
	os.Exit(1)
}
