package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestTCPBoundConnection(t *testing.T) {
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var loop net.Interface
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			loop = iface
			break
		}
	}
	if loop.Index == 0 {
		t.Fatal("no loopback")
	}
	for _, address := range []string{"127.0.0.1", "::1"} {
		t.Run(address, func(t *testing.T) {
			listener, err := net.Listen("tcp", net.JoinHostPort(address, "0"))
			if err != nil {
				t.Skip(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			defer listener.Close()
			go func() {
				conn, err := listener.Accept()
				if err == nil {
					conn.Close()
				}
			}()
			// Neither proxy environment variable may redirect measurement to an HTTP proxy.
			t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1")
			t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
			ip := netip.MustParseAddr(address)
			delay, ok := tcpConnect(ip, port, time.Second, loop, address)
			if !ok || delay <= 0 {
				t.Fatalf("bound connect failed: %v, %v", delay, ok)
			}

			invalid := loop
			invalid.Index = 999999
			invalid.Name = "nonexistent-ip-list-test"
			if _, ok = tcpConnect(ip, port, time.Second, invalid, address); ok {
				t.Fatal("unbound fallback accepted")
			}
			if _, ok = tcpPing(ip, port, time.Second, invalid.Name); ok {
				t.Fatal("missing interface accepted")
			}
			listener.Close()
			if _, ok = tcpConnect(ip, port, time.Second, loop, address); ok {
				t.Fatal("closed port counted as success")
			}
		})
	}
}

func TestTCPReportMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Probe-Features") != "tcp-ping-v1" {
			t.Error("missing capability")
		}
		var body struct {
			Mode string `json:"mode"`
			Port int    `json:"tcp_port"`
		}
		// Accept results field here, inspect measurement identity.
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Mode != "tcp" || body.Port != 8443 {
			t.Errorf("wrong metadata: %+v", body)
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	if err := report(server.Client(), server.URL, "secret", []result{}, settings{Mode: "tcp", TCPPort: 8443}); err != nil {
		t.Fatal(err)
	}
}
