package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/component/ca"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/listener/inbound"
)

const testUUID = "11111111-1111-4111-8111-111111111111"

func TestShareLinkParameters(t *testing.T) {
	for _, transport := range []string{"httpupgrade", "xhttp", "grpc"} {
		t.Run(transport, func(t *testing.T) {
			raw := "vless://" + testUUID + "@original.example:443?encryption=none&security=tls&sni=sni.example&type=" + transport + "&path=%2Fhu&host=host.example&mode=packet-up&alpn=h2&serviceName=svc#Test"
			if transport == "grpc" {
				raw = strings.Replace(raw, "host=host.example", "host=sni.example", 1)
			}
			config, _, err := parseShare(raw)
			if err != nil {
				t.Fatal(err)
			}
			config["server"] = "192.0.2.1"
			encoded, _ := json.Marshal(config)
			s := string(encoded)
			if config["servername"] != "sni.example" || !strings.Contains(s, "h2") {
				t.Fatalf("SNI/ALPN lost: %s", s)
			}
			switch transport {
			case "httpupgrade":
				if !strings.Contains(s, "host.example") || !strings.Contains(s, "/hu") {
					t.Fatal(s)
				}
			case "xhttp":
				if !strings.Contains(s, "packet-up") || !strings.Contains(s, "host.example") || !strings.Contains(s, "/hu") {
					t.Fatal(s)
				}
			case "grpc":
				if !strings.Contains(s, "svc") {
					t.Fatal(s)
				}
			}
		})
	}
	config, _, err := parseShare("vless://" + testUUID + "@original.example:443?security=tls&type=ws")
	if err != nil || config["servername"] != "original.example" {
		t.Fatal("implicit SNI not preserved", err)
	}
	for _, suffix := range []string{"&unknown=true", "&security=none", "&encryption=unsupported", "&type=kcp"} {
		_, _, err := parseShare("vless://" + testUUID + "@original.example:443?security=tls" + suffix)
		if err == nil {
			t.Fatalf("accepted invalid parameters %s", suffix)
		}
	}
}
func TestExpandPrefixes(t *testing.T) {
	ips, err := expandPrefixes([]string{"192.0.2.0/30", "192.0.2.1/32", "2001:db8::/127"})
	if err != nil || len(ips) != 6 {
		t.Fatalf("unexpected %v %v", ips, err)
	}
	for _, cidr := range []string{"0.0.0.0/0", "2001:db8::/32", "192.0.2.1/24"} {
		if _, err := expandPrefixes([]string{cidr}); err == nil {
			t.Fatal("accepted", cidr)
		}
	}
	ips, err = expandPrefixes([]string{"255.255.255.255/32", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff/128"})
	if err != nil || len(ips) != 2 {
		t.Fatal("address boundary failed", err)
	}
}

// A real mihomo VLESS server terminates the proxy transport. The tunnel only
// supplies a deterministic HTTP response, keeping tests independent of WANs.
type probeTestTunnel struct {
	status atomic.Int32
	hits   atomic.Int32
}

func (tt *probeTestTunnel) HandleTCPConn(conn net.Conn, metadata *C.Metadata) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	request, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}
	defer request.Body.Close()
	if metadata.Host != "probe.example" || request.Method != "HEAD" {
		return
	}
	tt.hits.Add(1)
	fmt.Fprintf(conn, "HTTP/1.1 %d Test\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", tt.status.Load())
}
func (*probeTestTunnel) HandleUDPPacket(packet C.UDPPacket, _ *C.Metadata) { packet.Drop() }
func (*probeTestTunnel) NatTable() C.NatTable                              { return nil }
func TestProbeThroughRealMihomoTransports(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"sni.example"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err = ca.AddCertificate(certPEM); err != nil {
		t.Fatal(err)
	}
	defer ca.ResetCertificate()
	for _, transport := range []string{"httpupgrade", "xhttp", "grpc"} {
		t.Run(transport, func(t *testing.T) {
			options := inbound.VlessOption{BaseOption: inbound.BaseOption{NameStr: "test", Listen: "127.0.0.1", Port: "0"}, Users: []inbound.VlessUser{{Username: "test", UUID: testUUID}}, Certificate: certPEM, PrivateKey: keyPEM}
			switch transport {
			case "httpupgrade":
				options.WsPath = "/hu"
			case "xhttp":
				options.XHTTPConfig = inbound.XHTTPConfig{Path: "/hu", Host: "host.example", Mode: "packet-up"}
			case "grpc":
				options.GrpcServiceName = "svc"
			}
			server, err := inbound.NewVless(&options)
			if err != nil {
				t.Fatal(err)
			}
			tunnel := &probeTestTunnel{}
			tunnel.status.Store(204)
			if err = server.Listen(tunnel); err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			addr, err := netip.ParseAddrPort(server.Address())
			if err != nil {
				t.Fatal(err)
			}
			raw := fmt.Sprintf("vless://%s@not-resolvable.invalid:%d?encryption=none&security=tls&sni=sni.example&type=%s&path=%%2Fhu&host=host.example&mode=packet-up&serviceName=svc", testUUID, addr.Port(), transport)
			if transport == "grpc" {
				raw = strings.Replace(raw, "host=host.example", "host=sni.example", 1)
				raw += "&alpn=h2"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			template, _, err := parseShare(raw)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(template)
			delay, err := probeIP(ctx, template, addr.Addr(), "http://probe.example/generate_204")
			if err != nil || delay < 1 || tunnel.hits.Load() != 1 {
				t.Fatalf("probe did not traverse proxy: delay=%d hits=%d err=%v", delay, tunnel.hits.Load(), err)
			}
			tunnel.status.Store(503)
			if delay, err = probeIP(ctx, template, addr.Addr(), "http://probe.example/generate_204"); err != nil || delay < 1 {
				t.Fatalf("reachable proxy rejected for HTTP status: delay=%d err=%v", delay, err)
			}
			// Reuse one template concurrently; every request must use its own proxy.
			var workers sync.WaitGroup
			for i := range 16 {
				workers.Go(func() {
					ip := addr.Addr()
					if i%2 != 0 {
						ip = netip.MustParseAddr("127.0.0.2")
					}
					_, err := probeIP(ctx, template, ip, "http://probe.example/")
					if (err == nil) != (i%2 == 0) {
						t.Errorf("concurrent probe to %s: %v", ip, err)
					}
				})
			}
			workers.Wait()
			after, _ := json.Marshal(template)
			if string(before) != string(after) {
				t.Fatal("shared template was mutated")
			}
			if tunnel.hits.Load() != 10 {
				t.Fatalf("lost requests: %d", tunnel.hits.Load())
			}
			wrong := strings.Replace(raw, "sni=sni.example", "sni=wrong.example", 1)
			if transport == "grpc" {
				wrong = strings.Replace(wrong, "host=sni.example", "host=wrong.example", 1)
			}
			invalid, _, err := parseShare(wrong)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = probeIP(ctx, invalid, addr.Addr(), "http://probe.example/"); err == nil {
				t.Fatal("bad TLS name accepted")
			}
		})
	}
}

// Compare the previous per-IP setup with reusing a task's validated template.
// No network I/O: isolates CPU and allocation costs from WAN latency.
func BenchmarkProbeSetup(b *testing.B) {
	for _, transport := range []string{"httpupgrade", "xhttp", "grpc"} {
		raw := "vless://" + testUUID + "@original.example:443?security=tls&sni=sni.example&host=sni.example&type=" + transport + "&path=%2Fhu&mode=packet-up&serviceName=svc"
		template, _, err := parseShare(raw)
		if err != nil {
			b.Fatal(err)
		}
		ip := netip.MustParseAddr("192.0.2.1")
		b.Run(transport+"/per_ip_parse", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				config, _, err := parseShare(raw)
				if err != nil {
					b.Fatal(err)
				}
				config["server"] = ip.String()
				proxy, err := adapter.ParseProxy(config)
				if err != nil {
					b.Fatal(err)
				}
				_ = proxy.Close()
			}
		})
		b.Run(transport+"/task_template", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				proxy, err := newProbe(template, ip)
				if err != nil {
					b.Fatal(err)
				}
				_ = proxy.Close()
			}
		})
	}
}

// The selected physical interface must reach mihomo, without changing the
// reusable node template or the Host/SNI used for each substituted address.
func TestProbeInterface(t *testing.T) {
	t.Setenv("PROBE_INTERFACE", "test-egress")
	template, _, err := parseShare("vless://" + testUUID + "@original.example:443?security=tls&type=xhttp&host=front.example&sni=front.example")
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := newProbe(template, netip.MustParseAddr("192.0.2.7"))
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	if proxy.ProxyInfo().Interface != "test-egress" {
		t.Fatal("probe ignored interface binding")
	}
	if template["server"] != "original.example" || template["interface-name"] != nil {
		t.Fatal("probe modified shared template")
	}
}
