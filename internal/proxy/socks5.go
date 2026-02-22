package proxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// DialSocks5 creates a TCP tunnel to targetAddr through a SOCKS5 server.
func DialSocks5(ctx context.Context, socksAddr, targetAddr string, timeout time.Duration) (net.Conn, error) {
	d := &net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", socksAddr)
	if err != nil {
		return nil, fmt.Errorf("dial socks server: %w", err)
	}
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("socks greeting write: %w", err)
	}

	resp := make([]byte, 2)
	if _, err := readFull(conn, resp); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("socks greeting read: %w", err)
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		_ = conn.Close()
		return nil, errors.New("socks auth negotiation failed")
	}

	host, portStr, err := net.SplitHostPort(targetAddr)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("target addr parse: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		_ = conn.Close()
		return nil, errors.New("target port is invalid")
	}

	req := []byte{0x05, 0x01, 0x00}
	ip := net.ParseIP(host)
	switch {
	case ip != nil && ip.To4() != nil:
		req = append(req, 0x01)
		req = append(req, ip.To4()...)
	case ip != nil && ip.To16() != nil:
		req = append(req, 0x04)
		req = append(req, ip.To16()...)
	default:
		host = strings.TrimSpace(host)
		if len(host) == 0 || len(host) > 255 {
			_ = conn.Close()
			return nil, errors.New("target host is invalid")
		}
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	pb := make([]byte, 2)
	binary.BigEndian.PutUint16(pb, uint16(port))
	req = append(req, pb...)

	if _, err := conn.Write(req); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("socks connect write: %w", err)
	}

	br := bufio.NewReader(conn)
	head := make([]byte, 4)
	if _, err := readFull(br, head); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("socks connect read: %w", err)
	}
	if head[0] != 0x05 {
		_ = conn.Close()
		return nil, errors.New("invalid socks version in reply")
	}
	if head[1] != 0x00 {
		_ = conn.Close()
		return nil, fmt.Errorf("socks connect failed, code=0x%x", head[1])
	}

	var skip int
	switch head[3] {
	case 0x01:
		skip = 4
	case 0x04:
		skip = 16
	case 0x03:
		l, err := br.ReadByte()
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("socks reply domain len read: %w", err)
		}
		skip = int(l)
	default:
		_ = conn.Close()
		return nil, errors.New("socks reply had unknown address type")
	}
	trash := make([]byte, skip+2)
	if _, err := readFull(br, trash); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("socks reply tail read: %w", err)
	}

	if timeout > 0 {
		_ = conn.SetDeadline(time.Time{})
	}
	return conn, nil
}

func readFull(r interface{ Read([]byte) (int, error) }, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		k, err := r.Read(b[n:])
		n += k
		if err != nil {
			return n, err
		}
	}
	return n, nil
}
