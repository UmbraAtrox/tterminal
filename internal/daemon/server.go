package daemon

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uatx/tterminal/internal/config"
	"github.com/uatx/tterminal/internal/session"
	"github.com/uatx/tterminal/internal/types"
)

type Server struct {
	cfg      *config.Config
	manager  *session.Manager
	mcp      *mcp.Server
	listener net.Listener

	mu         sync.RWMutex
	identities map[string]types.Identity
	logger     *slog.Logger
}

type handshake struct {
	Token string `json:"token"`
}

type readerCloser struct {
	io.Reader
	io.Closer
}

func NewServer(cfg *config.Config, manager *session.Manager) *Server {
	level := slog.LevelInfo
	switch cfg.Daemon.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	s := &Server{
		cfg:        cfg,
		manager:    manager,
		identities: map[string]types.Identity{},
		logger:     logger,
	}
	s.mcp = newMCPServer(s)
	return s
}

func (s *Server) Serve(ctx context.Context) error {
	socket := config.ExpandHome(s.cfg.Daemon.SocketPath)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return fmt.Errorf("socket_dir: %w", err)
	}
	if _, err := os.Stat(socket); err == nil {
		conn, dialErr := net.DialTimeout("unix", socket, 300*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			return fmt.Errorf("daemon_already_running: %s", socket)
		}
		if err := os.Remove(socket); err != nil {
			return fmt.Errorf("socket_stale: %w", err)
		}
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("socket_listen: %w", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		listener.Close()
		return fmt.Errorf("socket_mode: %w", err)
	}
	s.listener = listener
	if pidFile := config.ExpandHome(s.cfg.Daemon.PIDFile); pidFile != "" {
		if err := os.MkdirAll(filepath.Dir(pidFile), 0o700); err == nil {
			if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
				s.logger.Warn("pid_file", "error", err)
			}
		} else {
			s.logger.Warn("pid_dir", "error", err)
		}
	}

	go func() {
		<-ctx.Done()
		s.Close()
	}()

	s.logger.Info("daemon_ready", "socket", socket)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || err == net.ErrClosed {
				return nil
			}
			return fmt.Errorf("socket_accept: %w", err)
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) Close() error {
	var err error
	if s.listener != nil {
		err = s.listener.Close()
	}
	if s.cfg.Daemon.SocketPath != "" {
		_ = os.Remove(config.ExpandHome(s.cfg.Daemon.SocketPath))
	}
	if s.cfg.Daemon.PIDFile != "" {
		_ = os.Remove(config.ExpandHome(s.cfg.Daemon.PIDFile))
	}
	return err
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		conn.Close()
		return
	}
	var auth handshake
	if err := json.Unmarshal([]byte(line), &auth); err != nil {
		conn.Close()
		return
	}
	if !s.tokenValid(auth.Token) {
		conn.Close()
		return
	}
	conn.SetDeadline(time.Time{})

	identity, err := peerIdentity(conn)
	if err != nil {
		s.logger.Warn("peer_identity", "error", err)
		conn.Close()
		return
	}
	if s.cfg.RoleFor(identity.UID) == types.RoleDeny {
		conn.Close()
		return
	}

	transport := &mcp.IOTransport{Reader: readerCloser{Reader: reader, Closer: conn}, Writer: conn}
	connCtx, cancel := context.WithCancel(ctx)
	session, err := s.mcp.Connect(connCtx, transport, nil)
	if err != nil {
		cancel()
		conn.Close()
		return
	}
	s.remember(session.ID(), identity)
	go func() {
		_ = session.Wait()
		cancel()
		s.forget(session.ID())
	}()
}

func (s *Server) tokenValid(token string) bool {
	expected := s.cfg.Daemon.Token
	if expected == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

func (s *Server) remember(id string, identity types.Identity) {
	s.mu.Lock()
	s.identities[id] = identity
	s.mu.Unlock()
}

func (s *Server) forget(id string) {
	s.mu.Lock()
	delete(s.identities, id)
	s.mu.Unlock()
}

func (s *Server) identity(id string) (types.Identity, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	identity, ok := s.identities[id]
	return identity, ok
}

func peerIdentity(conn net.Conn) (types.Identity, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return types.Identity{UID: os.Getuid(), GID: os.Getgid(), PID: os.Getpid()}, nil
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return types.Identity{}, err
	}
	var cred *syscall.Ucred
	var credErr error
	err = raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil {
		return types.Identity{}, err
	}
	if credErr != nil {
		return types.Identity{}, credErr
	}
	if cred == nil {
		return types.Identity{}, fmt.Errorf("peer credentials unavailable")
	}
	return types.Identity{UID: int(cred.Uid), GID: int(cred.Gid), PID: int(cred.Pid)}, nil
}
