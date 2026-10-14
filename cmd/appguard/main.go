package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"appguard/internal/buildinfo"
	"appguard/internal/config"
	"appguard/internal/control"
	"appguard/internal/service"
	"appguard/internal/setup"
	"appguard/internal/update"
	"appguard/internal/watchdog"
)

const serviceName = "AppGuard"

func main() {
	command, configPath, svcName := parseArgs(os.Args[1:])

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
	case "setup":
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		must(setup.Run(ctx, configPath))
		fmt.Println("AppGuard configuration saved:", configPath)
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
	case "service-restart":
		// Internal: launched detached by an agent that installed an update.
		must(service.RestartWhenStopped(svcName, 2*time.Minute))
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
			return runAgent(ctx, configPath, newServiceUpdater())
		}))
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		slog.Info("no configuration found; opening first-run setup", "config", configPath)
		must(setup.Run(ctx, configPath))
	}

	cfg, err := config.Load(configPath)
	must(err)
	if cfg.Role == "controller" {
		must(runControllerNode(ctx, configPath))
		return
	}
	must(runAgent(ctx, configPath, nil))
}

func parseArgs(args []string) (command, configPath, svcName string) {
	command = "run"
	configPath = defaultConfigPath()
	svcName = serviceName
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-config", "--config":
			if i+1 < len(args) {
				configPath = args[i+1]
				i++
			}
		case "-service", "--service":
			if i+1 < len(args) {
				svcName = args[i+1]
				i++
			}
		default:
			if args[i] != "" && args[i][0] != '-' { command = args[i] }
		}
	}
	return
}

func defaultConfigPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "appguard.json"
	}
	return filepath.Join(filepath.Dir(exe), "appguard.json")
}

// newServiceUpdater prepares self-update, which is only enabled for an
// agent running as a Windows service. It returns nil when the running binary
// cannot be inspected.
func newServiceUpdater() *update.Updater {
	u, err := update.New(buildinfo.Version)
	if err != nil {
		slog.Warn("self-update disabled", "error", err)
		return nil
	}
	return u
}

func runAgent(ctx context.Context, configPath string, upd *update.Updater) error {
	abs, _ := filepath.Abs(configPath)
	slog.Info("starting AppGuard", "config", abs)

	ctx, stop := context.WithCancel(ctx)
	defer stop()

	// If the restart helper cannot be launched, exit with an error so the
	// service recovery actions start the (new or restored) binary instead.
	var helperFailed atomic.Bool
	restart := func() {
		if err := service.ScheduleRestart(serviceName, configPath); err != nil {
			slog.Error("could not schedule service restart; relying on service recovery", "error", err)
			helperFailed.Store(true)
		}
		stop()
	}
	restartErr := func() error {
		if helperFailed.Load() {
			return errors.New("restart after self-update")
		}
		return nil
	}

	var opts control.AgentOptions
	if upd != nil {
		rolledBack, err := upd.Startup()
		if err != nil {
			slog.Error("self-update startup check failed", "error", err)
		}
		if rolledBack {
			slog.Warn("updated binary kept failing; previous version restored, restarting")
			restart()
			return restartErr()
		}
		opts = control.AgentOptions{Updater: upd, Restart: restart}
	}

	cfg, err := config.Load(configPath)
	if err != nil { return err }
	w := watchdog.New(cfg, configPath)
	if cfg.Controller.URL != "" { go control.RunAgentClient(ctx, cfg, w, opts) }
	if err := w.Run(ctx); err != nil { return err }
	return restartErr()
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

	go func() {
		client := &http.Client{Timeout: 1 * time.Second}
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			resp, err := client.Get(strings.TrimRight(cfg.Controller.URL, "/") + "/healthz")
			if err == nil && resp.StatusCode == http.StatusOK {
				_ = resp.Body.Close()
				control.RunAgentClient(ctx, cfg, w, control.AgentOptions{})
				return
			}
			if resp != nil {
				_ = resp.Body.Close()
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()

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
