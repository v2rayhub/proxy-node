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
	CorePath string
	Port     int
	Timeout  time.Duration
}

type Started struct {
	Cmd        *exec.Cmd
	ConfigPath string
	LogPath    string
}

func (r Runner) Start(ctx context.Context, outbound map[string]any) (*Started, error) {
	if r.CorePath == "" {
		return nil, fmt.Errorf("core path is required")
	}
	if r.Port == 0 {
		return nil, fmt.Errorf("local socks port is required")
	}

	cfg := map[string]any{
		"log": map[string]any{
			"loglevel": "warning",
		},
		"inbounds": []any{map[string]any{
			"listen":   "127.0.0.1",
			"port":     r.Port,
			"protocol": "socks",
			"settings": map[string]any{"udp": false},
		}},
		"outbounds": []any{
			outbound,
			map[string]any{"tag": "direct", "protocol": "freedom"},
		},
	}

	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal core config: %w", err)
	}

	dir, err := os.MkdirTemp("", "health-node-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	configPath := filepath.Join(dir, "config.json")
	logPath := filepath.Join(dir, "core.log")
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

	return &Started{Cmd: cmd, ConfigPath: configPath, LogPath: logPath}, nil
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
