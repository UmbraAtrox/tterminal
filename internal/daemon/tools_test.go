package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uatx/tterminal/internal/config"
	"github.com/uatx/tterminal/internal/session"
)

func TestMCPToolFlow(t *testing.T) {
	cfg := config.Default()
	cfg.Security.DefaultRole = "owner"
	manager := session.NewManager(cfg)
	defer manager.Shutdown()
	server := NewServer(cfg, manager)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.mcp.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	tools, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 13 {
		t.Fatalf("tools = %d, want 13", len(tools.Tools))
	}

	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if result.IsError {
			t.Fatalf("%s: %s", name, errorText(result))
		}
		return result
	}

	openResult := call("term_open", map[string]any{
		"backend": "pty",
		"pty": map[string]any{
			"argv": []string{"/bin/sh", "-c", "printf MCP_OK; sleep 10"},
		},
		"cols": 80,
		"rows": 24,
	})
	var opened openOut
	decodeStructured(t, openResult, &opened)
	if opened.ID == "" {
		t.Fatal("empty session id")
	}

	call("term_read", map[string]any{
		"session_id": opened.ID,
		"wait_ms":    2000,
		"max_bytes":  4096,
	})
	waitResult := call("term_wait", map[string]any{
		"session_id": opened.ID,
		"until":      "regex",
		"pattern":    "MCP_OK",
		"timeout_ms": 2000,
	})
	var waited waitOut
	decodeStructured(t, waitResult, &waited)
	if waited.Reason != "regex" {
		t.Fatalf("wait reason = %q", waited.Reason)
	}
	if !strings.Contains(waited.Data, "MCP_OK") {
		t.Fatalf("wait data = %q", waited.Data)
	}
	call("term_signal", map[string]any{"session_id": opened.ID, "signal": "KILL"})
	call("term_close", map[string]any{"session_id": opened.ID})
}

func TestMCPOutputWindowModes(t *testing.T) {
	cfg := config.Default()
	cfg.Security.DefaultRole = "owner"
	cfg.Limits.ResponseMaxBytes = 16
	manager := session.NewManager(cfg)
	defer manager.Shutdown()
	server := NewServer(cfg, manager)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.mcp.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "output-window-test-client", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if result.IsError {
			t.Fatalf("%s: %s", name, errorText(result))
		}
		return result
	}

	openedResult := call("term_open", map[string]any{
		"backend": "pty",
		"pty":     map[string]any{"argv": []string{"/bin/sh", "-i"}},
	})
	var opened openOut
	decodeStructured(t, openedResult, &opened)

	var tail execOut
	decodeStructured(t, call("term_exec", map[string]any{
		"session_id": opened.ID,
		"command":    "printf '0123456789abcdefghijklmnopqrstuvwxyz'",
		"timeout_ms": 3000,
	}), &tail)
	if len(tail.Data) > 16 || tail.SkippedBytes <= 0 || !tail.Truncated {
		t.Fatalf("tail result = %+v", tail)
	}

	var delta execOut
	decodeStructured(t, call("term_exec", map[string]any{
		"session_id": opened.ID,
		"command":    "printf 'DELTA_OK'",
		"mode":       "delta",
		"cursor":     tail.RangeStartCursor,
		"timeout_ms": 3000,
	}), &delta)
	if !strings.Contains(delta.Data, "DELTA_OK") || delta.Truncated {
		t.Fatalf("delta result = %+v", delta)
	}

	var all execOut
	decodeStructured(t, call("term_exec", map[string]any{
		"session_id": opened.ID,
		"command":    "printf 'ALL_OK'",
		"mode":       "all",
		"cursor":     tail.RangeStartCursor,
		"timeout_ms": 3000,
	}), &all)
	if !strings.Contains(all.Data, "0123456789abcdefghijklmnopqrstuvwxyz") || !strings.Contains(all.Data, "ALL_OK") {
		t.Fatalf("all result = %+v", all)
	}
	call("term_close", map[string]any{"session_id": opened.ID})
}

func TestMCPScreenCap(t *testing.T) {
	cfg := config.Default()
	cfg.Security.DefaultRole = "owner"
	cfg.Limits.MaxScreenChars = 50
	manager := session.NewManager(cfg)
	defer manager.Shutdown()
	server := NewServer(cfg, manager)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.mcp.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "screen-cap-test-client", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if result.IsError {
			t.Fatalf("%s: %s", name, errorText(result))
		}
		return result
	}

	openedResult := call("term_open", map[string]any{
		"backend": "pty",
		"pty":     map[string]any{"argv": []string{"/bin/sh", "-i"}},
	})
	var opened openOut
	decodeStructured(t, openedResult, &opened)
	call("term_write", map[string]any{"session_id": opened.ID, "data": strings.Repeat("X", 200) + "\r"})
	var screenResult screenOut
	decodeStructured(t, call("term_screen", map[string]any{"session_id": opened.ID}), &screenResult)
	if len([]rune(screenResult.Screen.Text)) > cfg.Limits.MaxScreenChars {
		t.Fatalf("screen chars = %d", len([]rune(screenResult.Screen.Text)))
	}
	if !screenResult.Screen.Truncated {
		t.Fatal("expected truncated screen")
	}
	call("term_close", map[string]any{"session_id": opened.ID})
}

func decodeStructured(t *testing.T, result *mcp.CallToolResult, out any) {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("structured content = %s: %v", data, err)
	}
}

func errorText(result *mcp.CallToolResult) string {
	data, _ := json.Marshal(result.Content)
	return string(data)
}

func TestMCPTmuxFlow(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	socket := fmt.Sprintf("tterminal-mcp-test-%d-%d", os.Getuid(), time.Now().UnixNano())
	defer func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	}()

	cfg := config.Default()
	cfg.Security.DefaultRole = "owner"
	manager := session.NewManager(cfg)
	defer manager.Shutdown()
	server := NewServer(cfg, manager)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.mcp.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "tmux-test-client", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if result.IsError {
			t.Fatalf("%s: %s", name, errorText(result))
		}
		return result
	}

	openedResult := call("term_open", map[string]any{
		"backend": "tmux",
		"tmux": map[string]any{
			"socket_name":    socket,
			"session":        "tterminal-mcp-test",
			"create":         true,
			"command":        "printf 'TMUX_MCP_OK\\n'; sleep 30",
			"remain_on_exit": true,
		},
		"cols": 80,
		"rows": 24,
	})
	var opened openOut
	decodeStructured(t, openedResult, &opened)

	readResult := call("term_read", map[string]any{
		"session_id": opened.ID,
		"wait_ms":    5000,
		"max_bytes":  4096,
	})
	var read readOut
	decodeStructured(t, readResult, &read)
	if !strings.Contains(read.Data, "TMUX_MCP_OK") {
		t.Fatalf("read data = %q", read.Data)
	}
	call("term_resize", map[string]any{"session_id": opened.ID, "cols": 100, "rows": 30})
	call("term_signal", map[string]any{"session_id": opened.ID, "signal": "INT"})
	call("term_wait", map[string]any{"session_id": opened.ID, "until": "exit", "timeout_ms": 5000})
	call("term_close", map[string]any{"session_id": opened.ID})
}
