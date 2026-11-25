package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/uatx/tterminal/internal/backend"
	"github.com/uatx/tterminal/internal/config"
	"github.com/uatx/tterminal/internal/daemon"
	"github.com/uatx/tterminal/internal/mcpproxy"
	"github.com/uatx/tterminal/internal/session"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tterminal:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("missing command")
	}
	command := args[0]
	rest := args[1:]
	configPath := parseFlag(rest, "--config")
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	switch command {
	case "daemon":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		manager := session.NewManager(cfg)
		defer manager.Shutdown()
		server := daemon.NewServer(cfg, manager)
		return server.Serve(ctx)
	case "mcp":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return mcpproxy.Run(ctx, cfg)
	case "doctor":
		return doctor(cfg)
	case "help", "--help", "-h":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", command)
	}
}

func parseFlag(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, name+"=") {
			return strings.TrimPrefix(arg, name+"=")
		}
	}
	return ""
}

func doctor(cfg *config.Config) error {
	socket := config.ExpandHome(cfg.Daemon.SocketPath)
	fmt.Printf("config: %s\n", config.ConfigPath())
	fmt.Printf("socket: %s\n", socket)
	fmt.Printf("runtime: %s\n", config.RuntimeDir())
	if info, err := os.Stat(socket); err == nil {
		fmt.Printf("socket_state: present mode=%v\n", info.Mode().Perm())
	} else {
		fmt.Printf("socket_state: absent (%v)\n", err)
	}
	if _, err := exec.LookPath("tmux"); err == nil {
		fmt.Println("tmux: available")
	} else {
		fmt.Println("tmux: unavailable")
	}
	devices, err := backend.SerialDevices()
	if err != nil {
		fmt.Printf("serial: error %v\n", err)
	} else {
		fmt.Printf("serial_devices: %d\n", len(devices))
		for _, device := range devices {
			fmt.Printf("  %s\n", filepath.Clean(device))
		}
	}
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tterminal <daemon|mcp|doctor> [--config PATH]")
}
