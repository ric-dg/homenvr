package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ric-dg/homenvr/internal/config"
	"github.com/ric-dg/homenvr/internal/supervisor"
	"github.com/ric-dg/homenvr/internal/web"
)

// version is the v2 development version. Real releases follow v0.1.x from v1.
const version = "0.2.0-dev"

// restartExitCode tells the service manager (WinSW <onfailure
// action="restart"/>) that the process exited on purpose to be restarted by
// the web panel. Any non-zero code triggers the restart; this one is reserved.
const restartExitCode = 75

func main() {
	configPath := flag.String("config", "config.jsonc", "path to the JSONC config")
	yamlPath := flag.String("yaml", "", "go2rtc.yaml output path (default: <config dir>/go2rtc.yaml)")
	serviceName := flag.String("service", "homenvrd", "Windows service name (self-update uses it to restart the service)")
	dump := flag.Bool("dump-config", false, "print the effective config as JSON and exit")
	validate := flag.Bool("validate-config", false, "validate the config and exit")
	genYAML := flag.Bool("gen-yaml", false, "print the go2rtc.yaml the supervisor would write and exit")
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

	if *genYAML {
		fmt.Print(supervisor.BuildYAML(cfg))
		return
	}

	if *yamlPath == "" {
		*yamlPath = filepath.Join(filepath.Dir(*configPath), "go2rtc.yaml")
	}
	svc, err := supervisor.New(*configPath, *yamlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "homenvrd: %v\n", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var restartAfter, updateAfter bool
	panel := web.New(svc.CfgFile(), web.Options{
		ConfigPath:   *configPath,
		Status:       svc,
		Log:          svc.Log(),
		Version:      version,
		ServiceName:  *serviceName,
		OnShutdown:   stop,
		OnRestart:    func() { restartAfter = true; stop() },
		OnUpdate:     func() { updateAfter = true; stop() },
		RunRetention: svc.RunRetentionNow,
	})
	go func() {
		if err := panel.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "homenvrd: web: %v\n", err)
		}
	}()

	if err := svc.Run(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "homenvrd: %v\n", err)
		os.Exit(1)
	}
	if restartAfter {
		os.Exit(restartExitCode)
	}
	if updateAfter {
		// Clean exit: the service stops and the detached updater helper swaps
		// the exe and starts it again.
		os.Exit(0)
	}
}
