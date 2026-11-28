package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/uatx/tterminal/internal/types"
)

type Config struct {
	Daemon   DaemonConfig   `json:"daemon"`
	Limits   LimitsConfig   `json:"limits"`
	Security SecurityConfig `json:"security"`
}

type DaemonConfig struct {
	SocketPath string `json:"socket_path"`
	PIDFile    string `json:"pid_file"`
	Token      string `json:"token"`
	LogLevel   string `json:"log_level"`
}

type LimitsConfig struct {
	MaxSessions      int `json:"max_sessions"`
	MaxRingBytes     int `json:"max_ring_bytes"`
	MaxChunkBytes    int `json:"max_chunk_bytes"`
	ResponseMaxBytes int `json:"response_max_bytes"`
	ResponseMaxLines int `json:"response_max_lines"`
	MaxScreenChars   int `json:"max_screen_chars"`
	MaxCols          int `json:"max_cols"`
	MaxRows          int `json:"max_rows"`
	DefaultCols      int `json:"default_cols"`
	DefaultRows      int `json:"default_rows"`
	ReadTimeoutMS    int `json:"read_timeout_ms"`
	WaitTimeoutMS    int `json:"wait_timeout_ms"`
	SessionTTLMS     int `json:"session_ttl_ms"`
	QuietPollMS      int `json:"quiet_poll_ms"`
}

type SecurityConfig struct {
	DefaultRole           string            `json:"default_role"`
	Roles                 map[string]string `json:"roles"`
	AllowedRoots          []string          `json:"allowed_roots"`
	AllowedDevices        []string          `json:"allowed_devices"`
	AllowedExecutables    []string          `json:"allowed_executables"`
	DenyEnv               []string          `json:"deny_env"`
	AllowShellIntegration bool              `json:"allow_shell_integration"`
}

func Default() *Config {
	return &Config{
		Daemon: DaemonConfig{
			SocketPath: filepath.Join(RuntimeDir(), "tterminal", "daemon.sock"),
			PIDFile:    filepath.Join(RuntimeDir(), "tterminal", "daemon.pid"),
			LogLevel:   "info",
		},
		Limits: LimitsConfig{
			MaxSessions:      64,
			MaxRingBytes:     8 << 20,
			MaxChunkBytes:    1 << 20,
			ResponseMaxBytes: 64 << 10,
			ResponseMaxLines: 2000,
			MaxScreenChars:   20000,
			MaxCols:          400,
			MaxRows:          200,
			DefaultCols:      120,
			DefaultRows:      36,
			ReadTimeoutMS:    1000,
			WaitTimeoutMS:    30000,
			SessionTTLMS:     3600000,
			QuietPollMS:      25,
		},
		Security: SecurityConfig{
			DefaultRole:           "writer",
			Roles:                 map[string]string{},
			AllowedRoots:          []string{},
			AllowedDevices:        []string{},
			AllowedExecutables:    []string{},
			DenyEnv:               []string{"AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "GITHUB_TOKEN", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"},
			AllowShellIntegration: true,
		},
	}
}

func RuntimeDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(os.Getenv("HOME"), ".tterminal", "run")
}

func ConfigPath() string {
	if path := os.Getenv("TTERMINAL_CONFIG"); path != "" {
		return path
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "tterminal", "config.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tterminal", "config.json")
}

func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		path = ConfigPath()
	}
	data, err := os.ReadFile(ExpandHome(path))
	if err != nil {
		if os.IsNotExist(err) {
			cfg.applyEnv()
			return cfg, nil
		}
		return nil, fmt.Errorf("config_read: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config_parse: %w", err)
	}
	if cfg.Daemon.SocketPath == "" {
		cfg.Daemon.SocketPath = Default().Daemon.SocketPath
	}
	if cfg.Daemon.PIDFile == "" {
		cfg.Daemon.PIDFile = Default().Daemon.PIDFile
	}
	if cfg.Security.DefaultRole == "" {
		cfg.Security.DefaultRole = "writer"
	}
	if cfg.Security.Roles == nil {
		cfg.Security.Roles = map[string]string{}
	}
	cfg.applyEnv()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyEnv() {
	if token := os.Getenv("TTERMINAL_TOKEN"); token != "" {
		c.Daemon.Token = token
	}
	if socket := os.Getenv("TTERMINAL_SOCKET"); socket != "" {
		c.Daemon.SocketPath = ExpandHome(socket)
	}
	if level := os.Getenv("TTERMINAL_LOG_LEVEL"); level != "" {
		c.Daemon.LogLevel = level
	}
}

func (c *Config) Validate() error {
	if types.Role(c.Security.DefaultRole).Rank() == 0 {
		return fmt.Errorf("config: invalid default_role %q", c.Security.DefaultRole)
	}
	for uid, role := range c.Security.Roles {
		if _, err := strconv.Atoi(uid); err != nil {
			return fmt.Errorf("config: invalid uid key %q", uid)
		}
		if types.Role(role).Rank() == 0 {
			return fmt.Errorf("config: invalid role %q for uid %s", role, uid)
		}
	}
	if c.Limits.MaxRingBytes <= 0 || c.Limits.MaxChunkBytes <= 0 || c.Limits.MaxSessions <= 0 {
		return fmt.Errorf("config: limits must be positive")
	}
	if c.Limits.ResponseMaxBytes <= 0 || c.Limits.ResponseMaxLines <= 0 || c.Limits.MaxScreenChars <= 0 {
		return fmt.Errorf("config: response and screen limits must be positive")
	}
	if c.Limits.MaxCols <= 0 || c.Limits.MaxRows <= 0 {
		return fmt.Errorf("config: max_cols and max_rows must be positive")
	}
	if c.Limits.ResponseMaxBytes > c.Limits.MaxRingBytes {
		c.Limits.ResponseMaxBytes = c.Limits.MaxRingBytes
	}
	return nil
}

func (c *Config) RoleFor(uid int) types.Role {
	if uid == os.Getuid() {
		return types.RoleOwner
	}
	if role, ok := c.Security.Roles[strconv.Itoa(uid)]; ok {
		return types.Role(role)
	}
	return types.Role(strings.ToLower(c.Security.DefaultRole))
}

func (c *Config) IsRootAllowed(path string) bool {
	if len(c.Security.AllowedRoots) == 0 {
		return true
	}
	clean := filepath.Clean(path)
	for _, root := range c.Security.AllowedRoots {
		root = filepath.Clean(ExpandHome(root))
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (c *Config) IsDeviceAllowed(path string) bool {
	if len(c.Security.AllowedDevices) == 0 {
		return true
	}
	clean := filepath.Clean(path)
	for _, device := range c.Security.AllowedDevices {
		if clean == filepath.Clean(ExpandHome(device)) {
			return true
		}
	}
	return false
}

func (c *Config) IsExecutableAllowed(path string) bool {
	if len(c.Security.AllowedExecutables) == 0 {
		return true
	}
	clean := filepath.Clean(path)
	for _, exe := range c.Security.AllowedExecutables {
		if clean == filepath.Clean(ExpandHome(exe)) {
			return true
		}
	}
	return false
}

func (c *Config) IsEnvAllowed(key string) bool {
	for _, denied := range c.Security.DenyEnv {
		if strings.EqualFold(key, denied) {
			return false
		}
	}
	return true
}

func ExpandHome(path string) string {
	if path == "" || !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}
