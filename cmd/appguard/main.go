package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"appguard/internal/config"
	"appguard/internal/control"
	"appguard/internal/service"
	"appguard/internal/watchdog"
)

const serviceName = "AppGuard"

func main() {
	command, configPath := parseArgs(os.Args[1:])

	switch command {
	case "install":
		must(service.Install(serviceName, configPath))
		fmt.Println("AppGuard installed.")
		return
	case "uninstall":
		must(service.Uninstall(serviceName))
		fmt.Println("AppGuard uninstalled.")
		return
	case "start":
		must(service.Start(serviceName))
		fmt.Println("AppGuard service started.")
		return
	case "stop":
		must(service.Stop(serviceName))
		fmt.Println("AppGuard service stopped.")
		return
	case "status":
		st, err := service.Status(serviceName)
		must(err)
		fmt.Println(st)
		return
	case "controller":
		cfg, err := config.Load(configPath)
		must(err)
		must(control.RunController(cfg))
		return
	case "run":
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", command)
		os.Exit(2)
	}

	isService, err := service.IsWindowsService()
	must(err)
	if isService {
		must(service.Run(serviceName, func(ctx context.Context) error {
			return runAgent(ctx, configPath)
		}))
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	must(runAgent(ctx, configPath))
}

func parseArgs(args []string) (command, configPath string) {
	command = "run"
	configPath = "appguard.json"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-config", "--config":
			if i+1 < len(args) {
				configPath = args[i+1]
				i++
			}
		default:
			if args[i] != "" && args[i][0] != '-' { command = args[i] }
		}
	}
	return
}

func runAgent(ctx context.Context, configPath string) error {
	abs, _ := filepath.Abs(configPath)
	slog.Info("starting AppGuard", "config", abs)
	cfg, err := config.Load(configPath)
	if err != nil { return err }
	w := watchdog.New(cfg, configPath)
	if cfg.Controller.URL != "" { go control.RunAgentClient(ctx, cfg, w) }
	return w.Run(ctx)
}

func must(err error) {
	if err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}
