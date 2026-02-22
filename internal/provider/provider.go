package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Provider builds outbound configs for V2Ray-compatible cores.
type Provider interface {
	Outbound() (map[string]any, error)
	Name() string
}

type VLESS struct {
	Address  string
	Port     int
	ID       string
	Flow     string
	Network  string
	Security string
	Host     string
	Path     string
	SNI      string
	ALPN     string
	Service  string
}

type VMess struct {
	Address  string `json:"add"`
	Port     string `json:"port"`
	ID       string `json:"id"`
	AlterID  string `json:"aid"`
	Network  string `json:"net"`
	Host     string `json:"host"`
	Path     string `json:"path"`
	TLS      string `json:"tls"`
	SNI      string `json:"sni"`
	ALPN     string `json:"alpn"`
	Type     string `json:"type"`
	Security string `json:"scy"`
}

func FromURI(raw string) (Provider, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid URI: %w", err)
	}

	switch strings.ToLower(u.Scheme) {
	case "vless":
		return parseVLESS(u)
	case "vmess":
		return parseVMess(raw)
	default:
		return nil, fmt.Errorf("unsupported scheme %q (supported: vless, vmess)", u.Scheme)
	}
}

func parseVLESS(u *url.URL) (Provider, error) {
	if u.User == nil {
		return nil, errors.New("vless URI missing user id")
	}
	id := u.User.Username()
	if id == "" {
		return nil, errors.New("vless URI has empty user id")
	}

	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("vless host/port parse failed: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid vless port: %w", err)
	}

	q := u.Query()
	network := valueOrDefault(q.Get("type"), "tcp")
	security := valueOrDefault(q.Get("security"), "none")

	return &VLESS{
		Address:  host,
		Port:     port,
		ID:       id,
		Flow:     q.Get("flow"),
		Network:  network,
		Security: security,
		Host:     q.Get("host"),
		Path:     q.Get("path"),
		SNI:      q.Get("sni"),
		ALPN:     q.Get("alpn"),
		Service:  q.Get("serviceName"),
	}, nil
}

func parseVMess(raw string) (Provider, error) {
	const prefix = "vmess://"
	payload := strings.TrimPrefix(raw, prefix)
	if payload == raw {
		return nil, errors.New("invalid vmess URI")
	}

	decoded, err := decodeBase64Any(payload)
	if err != nil {
		return nil, fmt.Errorf("vmess base64 decode failed: %w", err)
	}

	var vm VMess
	if err := json.Unmarshal(decoded, &vm); err != nil {
		return nil, fmt.Errorf("vmess JSON decode failed: %w", err)
	}

	if vm.Address == "" || vm.Port == "" || vm.ID == "" {
		return nil, errors.New("vmess JSON missing add/port/id")
	}
	if vm.Network == "" {
		vm.Network = "tcp"
	}
	if vm.Security == "" {
		vm.Security = "auto"
	}
	return &vm, nil
}

func (v *VLESS) Name() string { return "vless" }

func (v *VLESS) Outbound() (map[string]any, error) {
	user := map[string]any{
		"id":         v.ID,
		"encryption": "none",
	}
	if v.Flow != "" {
		user["flow"] = v.Flow
	}

	out := map[string]any{
		"tag":      "proxy",
		"protocol": "vless",
		"settings": map[string]any{
			"vnext": []any{map[string]any{
				"address": v.Address,
				"port":    v.Port,
				"users":   []any{user},
			}},
		},
	}

	stream := map[string]any{
		"network":  v.Network,
		"security": v.Security,
	}
	if v.Network == "ws" {
		stream["wsSettings"] = map[string]any{
			"path": valueOrDefault(v.Path, "/"),
			"headers": map[string]any{
				"Host": v.Host,
			},
		}
	}
	if v.Network == "grpc" {
		stream["grpcSettings"] = map[string]any{
			"serviceName": v.Service,
		}
	}
	if v.Security == "tls" {
		stream["tlsSettings"] = map[string]any{
			"serverName": firstNonEmpty(v.SNI, v.Host, v.Address),
			"alpn":       splitCSV(v.ALPN),
		}
	}

	out["streamSettings"] = stream
	return out, nil
}

func (v *VMess) Name() string { return "vmess" }

func (v *VMess) Outbound() (map[string]any, error) {
	port, err := strconv.Atoi(v.Port)
	if err != nil {
		return nil, fmt.Errorf("invalid vmess port: %w", err)
	}
	alterID := 0
	if v.AlterID != "" {
		if a, err := strconv.Atoi(v.AlterID); err == nil {
			alterID = a
		}
	}

	out := map[string]any{
		"tag":      "proxy",
		"protocol": "vmess",
		"settings": map[string]any{
			"vnext": []any{map[string]any{
				"address": v.Address,
				"port":    port,
				"users": []any{map[string]any{
					"id":       v.ID,
					"alterId":  alterID,
					"security": valueOrDefault(v.Security, "auto"),
				}},
			}},
		},
	}

	security := "none"
	if strings.EqualFold(v.TLS, "tls") {
		security = "tls"
	}
	stream := map[string]any{
		"network":  valueOrDefault(v.Network, "tcp"),
		"security": security,
	}
	if v.Network == "ws" {
		stream["wsSettings"] = map[string]any{
			"path": valueOrDefault(v.Path, "/"),
			"headers": map[string]any{
				"Host": v.Host,
			},
		}
	}
	if security == "tls" {
		stream["tlsSettings"] = map[string]any{
			"serverName": firstNonEmpty(v.SNI, v.Host, v.Address),
			"alpn":       splitCSV(v.ALPN),
		}
	}
	out["streamSettings"] = stream
	return out, nil
}

func decodeBase64Any(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}

func valueOrDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
