package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"appguard/internal/buildinfo"
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
	case "version":
		fmt.Println("AppGuard", buildinfo.Version)
		return
	case "enroll":
		cfg, err := config.Load(configPath)
		must(err)
		if cfg.Controller.URL == "" {
			must(fmt.Errorf("controller.url is required for enrollment"))
		}
		must(control.Enroll(cfg))
		fmt.Println("AppGuard enrolled:", cfg.AgentID)
		return
	case "controller":
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		must(runControllerNode(ctx, configPath))
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
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			if cfg.Role == "controller" {
				return runControllerNode(ctx, configPath)
			}
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


func runControllerNode(ctx context.Context, configPath string) error {
	abs, _ := filepath.Abs(configPath)
	slog.Info("starting AppGuard controller node", "config", abs)

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	cfg.Role = "controller"

	if cfg.Controller.URL == "" {
		listen := cfg.Controller.Listen
		if listen == "" {
			listen = ":8443"
		}
		if strings.HasPrefix(listen, ":") {
			cfg.Controller.URL = "http://127.0.0.1" + listen
		} else {
			cfg.Controller.URL = "http://" + listen
		}
	}

	w := watchdog.New(cfg, configPath)

	errCh := make(chan error, 2)
	go func() {
		errCh <- control.RunController(cfg)
	}()
	go control.RunAgentClient(ctx, cfg, w)

	go func() {
		errCh <- w.Run(ctx)
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func must(err error) {
	if err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}
