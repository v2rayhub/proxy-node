# health-node

Small dependency-free Go CLI to validate V2Ray/Xray outbound configs (currently `vless://` and `vmess://`) on Linux VMs.

## Build

```bash
go build -o health-node ./cmd/health-node
```

## Requirements

- Linux VM
- V2Ray or Xray core binary available. Preferred packaging is to ship it with `health-node`.

## Bundle the core (recommended)

Put files in one folder:

```text
deploy/
  health-node
  xray
```

Then run without `--core`; the CLI auto-detects:
- `./xray` or `./v2ray` next to `health-node`
- `./core/xray` or `./core/v2ray` next to `health-node`
- `xray`/`v2ray` in `PATH` (fallback)

## Auto download/install core

You can let the CLI install core from GitHub releases:

```bash
./health-node install-core
```

Examples:

```bash
# default: latest from XTLS/Xray-core into current directory
./health-node install-core

# specific repo/tag and destination directory
./health-node install-core --repo v2fly/v2ray-core --version v5.20.0 --dest ./core
```

## Probe connectivity

```bash
./health-node probe \
  --uri 'vless://UUID@server.example.com:443?type=ws&security=tls&host=server.example.com&path=%2Fws&sni=server.example.com'
```

Output example:

```text
status=ok protocol=vless code=204 latency_ms=890 bytes=0
```

## Speed test

```bash
./health-node speed \
  --uri 'vmess://eyJhZGQiOiJzZXJ2ZXIuZXhhbXBsZS5jb20iLCJwb3J0IjoiNDQzIiwiaWQiOiJVVUlEIiwiYWlkIjoiMCIsIm5ldCI6IndzIiwiaG9zdCI6InNlcnZlci5leGFtcGxlLmNvbSIsInBhdGgiOiIvd3MiLCJ0bHMiOiJ0bHMifQ==' \
  --max-bytes 10485760
```

Output example:

```text
status=ok protocol=vmess bytes=10485760 elapsed_ms=4200 mbps=19.97
```

## Notes

- CLI generates a temporary core config with local SOCKS5 inbound at `127.0.0.1:<port>`.
- Only standard library is used.
- URI parser currently focuses on common VMess/VLESS fields (TCP/WS/gRPC and TLS basics).
- Architecture is split by package so you can add new providers (for example WireGuard) later.
- You can still override core path explicitly with `--core /path/to/xray`.
