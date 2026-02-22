package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"health-node/internal/core"
	"health-node/internal/installer"
	"health-node/internal/proxy"
	"health-node/internal/provider"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	sub := os.Args[1]
	switch sub {
	case "probe":
		if err := runProbe(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "probe failed: %v\n", err)
			os.Exit(1)
		}
	case "speed":
		if err := runSpeed(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "speed failed: %v\n", err)
			os.Exit(1)
		}
	case "install-core":
		if err := runInstallCore(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "install-core failed: %v\n", err)
			os.Exit(1)
		}
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", sub)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println(`health-node - v2ray/xray outbound health checker

Usage:
  health-node probe --uri <vless|vmess URI> [--core <path to xray/v2ray>]
  health-node speed --uri <vless|vmess URI> [--core <path to xray/v2ray>]

Commands:
  probe   Start core with generated config and run an HTTP probe through SOCKS5.
  speed   Start core and measure download speed through SOCKS5.
  install-core  Download and install Xray/V2Ray core from GitHub release.

Common flags:
  --uri string          VLESS/VMess URI
  --core string         core binary path (optional, auto-detected if empty)
  --local-socks int     local SOCKS port (default: random 20000-40000)
  --timeout duration    timeout for startup and checks (default: 20s)

Probe flags:
  --url string          probe URL (default: https://www.gstatic.com/generate_204)

Speed flags:
  --url string          download URL (default: https://speed.hetzner.de/10MB.bin)
  --max-bytes int       stop after N bytes (0 means full response)

Install-core flags:
  --repo string         GitHub repo owner/name (default: XTLS/Xray-core)
  --version string      release tag or "latest" (default: latest)
  --dest string         install directory (default: current dir)
  --force               overwrite existing binary if present
`)
}

func runProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	uri := fs.String("uri", "", "VLESS/VMess URI")
	corePath := fs.String("core", "", "core binary path")
	probeURL := fs.String("url", "https://www.gstatic.com/generate_204", "probe URL")
	timeout := fs.Duration("timeout", 20*time.Second, "timeout")
	localPort := fs.Int("local-socks", 0, "local socks port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *uri == "" {
		return errors.New("--uri is required")
	}
	resolvedCore, err := resolveCorePath(*corePath)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	prov, err := provider.FromURI(*uri)
	if err != nil {
		return err
	}
	outbound, err := prov.Outbound()
	if err != nil {
		return err
	}
	port := *localPort
	if port == 0 {
		port = randomPort()
	}

	r := core.Runner{CorePath: resolvedCore, Port: port, Timeout: *timeout}
	started, err := r.Start(ctx, outbound)
	if err != nil {
		return err
	}
	defer started.Stop()

	socksAddr := fmt.Sprintf("127.0.0.1:%d", port)
	if err := waitSocks(ctx, socksAddr, *timeout); err != nil {
		return fmt.Errorf("core did not become ready: %w\ncore log tail:\n%s", err, started.ReadLogTail())
	}

	latency, code, n, err := probeHTTP(ctx, socksAddr, *probeURL, *timeout)
	if err != nil {
		return fmt.Errorf("probe request failed: %w\ncore log tail:\n%s", err, started.ReadLogTail())
	}

	fmt.Printf("status=ok protocol=%s code=%d latency_ms=%d bytes=%d\n", prov.Name(), code, latency.Milliseconds(), n)
	return nil
}

func runSpeed(args []string) error {
	fs := flag.NewFlagSet("speed", flag.ContinueOnError)
	uri := fs.String("uri", "", "VLESS/VMess URI")
	corePath := fs.String("core", "", "core binary path")
	speedURL := fs.String("url", "https://speed.hetzner.de/10MB.bin", "speed test URL")
	maxBytes := fs.Int64("max-bytes", 10*1024*1024, "max bytes to download (0 for full)")
	timeout := fs.Duration("timeout", 45*time.Second, "timeout")
	localPort := fs.Int("local-socks", 0, "local socks port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *uri == "" {
		return errors.New("--uri is required")
	}
	resolvedCore, err := resolveCorePath(*corePath)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	prov, err := provider.FromURI(*uri)
	if err != nil {
		return err
	}
	outbound, err := prov.Outbound()
	if err != nil {
		return err
	}
	port := *localPort
	if port == 0 {
		port = randomPort()
	}

	r := core.Runner{CorePath: resolvedCore, Port: port, Timeout: *timeout}
	started, err := r.Start(ctx, outbound)
	if err != nil {
		return err
	}
	defer started.Stop()

	socksAddr := fmt.Sprintf("127.0.0.1:%d", port)
	if err := waitSocks(ctx, socksAddr, *timeout); err != nil {
		return fmt.Errorf("core did not become ready: %w\ncore log tail:\n%s", err, started.ReadLogTail())
	}

	bytesRead, elapsed, err := speedHTTP(ctx, socksAddr, *speedURL, *maxBytes, *timeout)
	if err != nil {
		return fmt.Errorf("speed request failed: %w\ncore log tail:\n%s", err, started.ReadLogTail())
	}
	mbps := (float64(bytesRead) * 8) / elapsed.Seconds() / 1_000_000
	fmt.Printf("status=ok protocol=%s bytes=%d elapsed_ms=%d mbps=%.2f\n", prov.Name(), bytesRead, elapsed.Milliseconds(), mbps)
	return nil
}

func runInstallCore(args []string) error {
	fs := flag.NewFlagSet("install-core", flag.ContinueOnError)
	repo := fs.String("repo", "XTLS/Xray-core", "GitHub repo owner/name")
	version := fs.String("version", "latest", "release tag or latest")
	dest := fs.String("dest", ".", "install directory")
	force := fs.Bool("force", false, "overwrite existing binary")
	timeout := fs.Duration("timeout", 2*time.Minute, "download/install timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	path, tag, err := installer.Install(ctx, installer.Options{
		Repo:    *repo,
		Version: *version,
		DestDir: *dest,
		Force:   *force,
	})
	if err != nil {
		return err
	}
	fmt.Printf("status=ok repo=%s version=%s installed=%s\n", *repo, tag, path)
	return nil
}

func waitSocks(ctx context.Context, socksAddr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return errors.New("timeout")
		}
		conn, err := net.DialTimeout("tcp", socksAddr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func probeHTTP(ctx context.Context, socksAddr, rawURL string, timeout time.Duration) (time.Duration, int, int64, error) {
	client := httpClientThroughSocks(socksAddr, timeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, 0, 0, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, 0, err
	}
	defer resp.Body.Close()
	n, _ := io.CopyN(io.Discard, resp.Body, 2048)
	return time.Since(start), resp.StatusCode, n, nil
}

func speedHTTP(ctx context.Context, socksAddr, rawURL string, maxBytes int64, timeout time.Duration) (int64, time.Duration, error) {
	client := httpClientThroughSocks(socksAddr, timeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, 0, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return 0, 0, fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}

	var n int64
	if maxBytes > 0 {
		n, err = io.CopyN(io.Discard, resp.Body, maxBytes)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, 0, err
		}
	} else {
		n, err = io.Copy(io.Discard, resp.Body)
		if err != nil {
			return 0, 0, err
		}
	}
	return n, time.Since(start), nil
}

func httpClientThroughSocks(socksAddr string, timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if !strings.EqualFold(network, "tcp") {
				return nil, fmt.Errorf("unsupported network %s", network)
			}
			return proxy.DialSocks5(ctx, socksAddr, addr, timeout)
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if _, err := url.Parse(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
}

func randomPort() int {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return 20000 + r.Intn(20000)
}

func resolveCorePath(flagPath string) (string, error) {
	// Explicit path always wins.
	if strings.TrimSpace(flagPath) != "" {
		return flagPath, nil
	}

	exePath, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exePath)
		candidates := []string{
			filepath.Join(exeDir, "xray"),
			filepath.Join(exeDir, "v2ray"),
			filepath.Join(exeDir, "core", "xray"),
			filepath.Join(exeDir, "core", "v2ray"),
		}
		for _, c := range candidates {
			if isExecutableFile(c) {
				return c, nil
			}
		}
	}

	// Fallback to PATH so existing setups still work.
	for _, name := range []string{"xray", "v2ray"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}

	return "", errors.New("core binary not found: place xray/v2ray next to health-node (or in ./core), or pass --core")
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}
