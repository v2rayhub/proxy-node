package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Runner struct {
	CorePath        string
	Port            int
	Timeout         time.Duration
	InboundProtocol string
	LogLevel        string
}

type Started struct {
	Cmd           *exec.Cmd
	ConfigPath    string
	LogPath       string
	AccessLogPath string
}

func (r Runner) Start(ctx context.Context, outbound map[string]any) (*Started, error) {
	if r.CorePath == "" {
		return nil, fmt.Errorf("core path is required")
	}
	if r.Port == 0 {
		return nil, fmt.Errorf("local socks port is required")
	}
	inboundProtocol := strings.TrimSpace(r.InboundProtocol)
	if inboundProtocol == "" {
		inboundProtocol = "socks"
	}
	if inboundProtocol != "socks" && inboundProtocol != "http" {
		return nil, fmt.Errorf("unsupported inbound protocol %q", inboundProtocol)
	}
	logLevel := strings.TrimSpace(r.LogLevel)
	if logLevel == "" {
		logLevel = "warning"
	}

	inbound := map[string]any{
		"listen":   "127.0.0.1",
		"port":     r.Port,
		"protocol": inboundProtocol,
	}
	if inboundProtocol == "socks" {
		inbound["settings"] = map[string]any{"udp": false}
	}

	cfg := map[string]any{
		"log": map[string]any{
			"loglevel": logLevel,
		},
		"inbounds": []any{inbound},
		"outbounds": []any{
			outbound,
			map[string]any{"tag": "direct", "protocol": "freedom"},
		},
	}

	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal core config: %w", err)
	}

	dir, err := os.MkdirTemp("", "proxy-node-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	configPath := filepath.Join(dir, "config.json")
	logPath := filepath.Join(dir, "core.log")
	accessLogPath := filepath.Join(dir, "access.log")

	cfg["log"] = map[string]any{
		"loglevel": logLevel,
		"access":   accessLogPath,
		"error":    logPath,
	}
	if err := os.WriteFile(configPath, body, 0o600); err != nil {
		return nil, fmt.Errorf("write config: %w", err)
	}

	logf, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("create log file: %w", err)
	}

	args := coreArgs(r.CorePath, configPath)
	cmd := exec.CommandContext(ctx, r.CorePath, args...)
	cmd.Stdout = logf
	cmd.Stderr = logf

	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		return nil, fmt.Errorf("start core: %w", err)
	}
	_ = logf.Close()

	return &Started{Cmd: cmd, ConfigPath: configPath, LogPath: logPath, AccessLogPath: accessLogPath}, nil
}

func (s *Started) Stop() {
	if s == nil || s.Cmd == nil || s.Cmd.Process == nil {
		return
	}
	_ = s.Cmd.Process.Kill()
	_, _ = s.Cmd.Process.Wait()
}

func (s *Started) ReadLogTail() string {
	if s == nil || s.LogPath == "" {
		return ""
	}
	b, err := os.ReadFile(s.LogPath)
	if err != nil {
		return ""
	}
	if len(b) > 4000 {
		b = b[len(b)-4000:]
	}
	return string(b)
}

func coreArgs(corePath, configPath string) []string {
	base := strings.ToLower(filepath.Base(corePath))
	if strings.Contains(base, "xray") {
		return []string{"run", "-c", configPath}
	}
	return []string{"-config", configPath}
}
