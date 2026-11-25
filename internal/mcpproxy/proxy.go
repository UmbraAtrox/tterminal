package mcpproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/uatx/tterminal/internal/config"
)

type handshake struct {
	Token string `json:"token"`
}

func Run(ctx context.Context, cfg *config.Config) error {
	conn, err := ensureDaemon(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()

	line, err := json.Marshal(handshake{Token: cfg.Daemon.Token})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := conn.Write(line); err != nil {
		return fmt.Errorf("daemon_auth: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := io.Copy(conn, os.Stdin)
		if unixConn, ok := conn.(*net.UnixConn); ok {
			_ = unixConn.CloseWrite()
		}
		if err != nil && runCtx.Err() == nil {
			errCh <- fmt.Errorf("stdin_copy: %w", err)
		}
		cancel()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := io.Copy(os.Stdout, conn)
		if err != nil && runCtx.Err() == nil {
			errCh <- fmt.Errorf("stdout_copy: %w", err)
		}
		cancel()
	}()

	go func() {
		<-runCtx.Done()
		conn.Close()
	}()
	wg.Wait()
	close(errCh)
	if err := <-errCh; err != nil {
		return err
	}
	return nil
}

func ensureDaemon(ctx context.Context, cfg *config.Config) (net.Conn, error) {
	conn, err := dial(cfg)
	if err == nil {
		return conn, nil
	}
	if err := startDaemon(cfg); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		conn, err := dial(cfg)
		if err == nil {
			return conn, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("daemon_unavailable: %w", err)
}

func dial(cfg *config.Config) (net.Conn, error) {
	socket := config.ExpandHome(cfg.Daemon.SocketPath)
	conn, err := net.DialTimeout("unix", socket, 400*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("daemon_unavailable: %w", err)
	}
	return conn, nil
}

func startDaemon(cfg *config.Config) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()
	logPath := filepath.Join(config.RuntimeDir(), "tterminal", "daemon.log")
	logFile := devNull
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err == nil {
		if opened, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			logFile = opened
			defer logFile.Close()
		}
	}
	cmd := exec.Command(executable, "daemon", "--foreground")
	cmd.Env = append(os.Environ(), "TTERMINAL_CONFIG="+config.ConfigPath())
	if cfg.Daemon.Token != "" {
		cmd.Env = append(cmd.Env, "TTERMINAL_TOKEN="+cfg.Daemon.Token)
	}
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("daemon_start: %w", err)
	}
	return nil
}
