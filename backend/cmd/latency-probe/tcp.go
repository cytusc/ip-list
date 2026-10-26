package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"time"
)

// Bind both the source address and the OS socket interface. Source-only binding
// does not bypass TUN routes. Never retry using an unbound socket.
func tcpPing(ip netip.Addr, port int, timeout time.Duration, name string) (float64, bool) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return 0, false
	}
	source := interfaceAddress(*iface, ip.Is6())
	if source == "" {
		return 0, false
	}
	return tcpConnect(ip, port, timeout, *iface, source)
}

func tcpConnect(ip netip.Addr, port int, timeout time.Duration, iface net.Interface, source string) (float64, bool) {
	if port < 1 || port > 65535 {
		return 0, false
	}
	network := "tcp4"
	if ip.Is6() {
		network = "tcp6"
	}
	dialer := net.Dialer{Timeout: timeout, LocalAddr: &net.TCPAddr{IP: net.ParseIP(source)},
		Control: func(_, _ string, raw syscall.RawConn) error {
			return bindSocket(raw, iface, ip.Is6())
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	elapsed := time.Since(start)
	if err != nil {
		return 0, false
	}
	conn.Close()
	return max(0.01, float64(elapsed)/float64(time.Millisecond)), true
}

func measurementLabel(cfg settings) string {
	if cfg.Mode == "tcp" {
		return fmt.Sprintf("TCP Ping :%d", cfg.TCPPort)
	}
	return "ICMP Ping"
}
