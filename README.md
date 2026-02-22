# health-node

Small CLI to check V2Ray/Xray outbound health (`vless://`, `vmess://`) and run speed tests.

## 1) Build

```bash
go build -o health-node ./cmd/health-node
```

## 2) Install core (no manual download needed)

Default (installs latest Xray core into current directory):

```bash
./health-node install-core
```

Other examples:

```bash
# install specific Xray tag
./health-node install-core --repo XTLS/Xray-core --version v26.2.6

# install v2ray-core into ./core directory
./health-node install-core --repo v2fly/v2ray-core --version v5.20.0 --dest ./core

# overwrite existing binary
./health-node install-core --force
```

After install, `probe` and `speed` auto-detect core from:
- same directory as `health-node` (`./xray` or `./v2ray`)
- `./core/xray` or `./core/v2ray`
- `PATH` (fallback)

## 3) Check connectivity

```bash
./health-node probe --uri 'vless://UUID@server.example.com:443?type=ws&security=tls&host=server.example.com&path=%2Fws&sni=server.example.com'
```

VMess example:

```bash
./health-node probe --uri 'vmess://BASE64_JSON'
```

## 4) Run speed test

```bash
./health-node speed --uri 'vmess://BASE64_JSON'
```

With options:

```bash
./health-node speed \
  --uri 'vmess://BASE64_JSON' \
  --url 'https://speed.hetzner.de/10MB.bin' \
  --max-bytes 10485760 \
  --timeout 45s
```

## 5) Open local proxy port (long-running)

SOCKS5 proxy (default):

```bash
./health-node proxy --uri 'vmess://BASE64_JSON' --local-port 1080
```

HTTP proxy:

```bash
./health-node proxy --uri 'vmess://BASE64_JSON' --inbound http --local-port 8080
```

`socks` is an alias of `proxy --inbound socks`.

Traffic UI behavior:
- In an interactive terminal, traffic is shown as a live dashboard (top-style refresh).
- In non-interactive output (logs/CI), traffic is printed as periodic lines.
- Disable with `--no-traffic`.

## Optional flags

```text
--core <path>         Explicit core path override
--local-socks <port>  Local SOCKS5 port (auto-random if not set)
--timeout <duration>  Command timeout (example: 20s, 1m)
```

## Help

```bash
./health-node --help
./health-node probe --help
./health-node speed --help
./health-node install-core --help
```

## Development Cycle

Typical local loop:

```bash
# 1) format (if gofmt is installed)
gofmt -w ./cmd ./internal

# 2) run tests
go test ./...

# 3) build binary
go build -o health-node ./cmd/health-node

# 4) smoke test help
./health-node --help
```

Targeted tests:

```bash
go test ./internal/provider -run VMess -v
```

## Architecture Notes

- `cmd/health-node/main.go`
Parses CLI args and orchestrates commands (`probe`, `speed`, `install-core`).

- `internal/provider`
Parses subscription URIs (`vless://`, `vmess://`) and converts them into outbound config blocks.

- `internal/core`
Creates temporary runtime config, starts/stops Xray/V2Ray process, and exposes log tail for errors.

- `internal/proxy`
Minimal SOCKS5 client used by HTTP probe/speed requests.

- `internal/installer`
Downloads matching core release from GitHub and installs executable locally.

## GitHub Release Automation

Tag push triggers a multi-platform release workflow (`.github/workflows/release.yml`) that builds and uploads downloadable assets.

Example:

```bash
git tag v0.1.0
git push origin v0.1.0
```

Release assets include Linux, macOS, and Windows binaries plus `checksums.txt`.
