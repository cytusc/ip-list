package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func TestCertificateChecks(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	makeCert := func(name string, serial int64, expired bool) tls.Certificate {
		until := time.Now().Add(time.Hour)
		if expired {
			until = time.Now().Add(-time.Minute)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: until, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, e := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
		if e != nil {
			t.Fatal(e)
		}
		return tls.Certificate{Certificate: [][]byte{der, ca.Raw}, PrivateKey: key}
	}
	good, bad, expired := makeCert("entry.example", 2, false), makeCert("www.paypal.com", 3, false), makeCert("entry.example", 4, true)
	for _, tc := range []struct {
		name     string
		badAt    int32
		cert     tls.Certificate
		trust    bool
		want     string
		attempts int
	}{
		{"valid", 0, good, true, "passed", 3},
		{"intermittent wrong certificate", 2, bad, true, "invalid", 2},
		{"wrong name", 1, bad, true, "invalid", 1},
		{"expired", 1, expired, true, "invalid", 1},
		{"untrusted", 0, good, false, "invalid", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			server.TLS = &tls.Config{GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				if hello.ServerName != "entry.example" {
					t.Errorf("SNI=%s", hello.ServerName)
				}
				call := calls.Add(1)
				if tc.badAt > 0 && call >= tc.badAt {
					return &tc.cert, nil
				}
				return &good, nil
			}}
			server.StartTLS()
			defer server.Close()
			_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
			template := map[string]any{"tls": true, "servername": "entry.example", "server": "must-not-resolve.invalid", "port": port}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			pool := roots
			if !tc.trust {
				pool = x509.NewCertPool()
			}
			result, e := checkCertificateWithRoots(ctx, template, netip.MustParseAddr("127.0.0.1"), pool)
			if result.Status != tc.want || result.Attempts != tc.attempts || (e == nil) != (tc.want == "passed") {
				t.Fatalf("result=%+v err=%v", result, e)
			}
			if tc.want == "invalid" && (result.Subject == "" || result.Error == "") {
				t.Fatalf("missing certificate diagnostic: %+v", result)
			}
		})
	}
	t.Run("timeout is not certificate error", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		go func() {
			c, e := listener.Accept()
			if e == nil {
				defer c.Close()
				_, _ = io.Copy(io.Discard, c)
			}
		}()
		_, port, _ := net.SplitHostPort(listener.Addr().String())
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		result, err := checkCertificateWithRoots(ctx, map[string]any{"tls": true, "servername": "entry.example", "port": port}, netip.MustParseAddr("127.0.0.1"), roots)
		if err == nil || result.Status != "error" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

func TestCertificateTLSShareTemplates(t *testing.T) {
	for _, transport := range []string{"tcp", "ws", "grpc", "xhttp", "httpupgrade"} {
		template, _, err := parseShare("vless://" + testUUID + "@entry.example:443?security=tls&type=" + transport)
		if err != nil {
			t.Fatal(err)
		}
		if template["tls"] != true || template["servername"] != "entry.example" {
			t.Fatalf("TLS detection bypassed for %s: %v", transport, template["tls"])
		}
	}
	template, _, err := parseShare("trojan://secret@entry.example:443?type=ws")
	if err != nil || template["tls"] != true || template["servername"] != "entry.example" {
		t.Fatalf("Trojan TLS detection bypassed: %v", err)
	}
	result, err := checkCertificate(context.Background(), map[string]any{"tls": true, "port": 443}, netip.MustParseAddr("127.0.0.1"))
	if err == nil || result.Status != "invalid" {
		t.Fatal("TLS without SNI accepted")
	}
	result, err = checkCertificate(context.Background(), map[string]any{"tls": false}, netip.MustParseAddr("127.0.0.1"))
	if err != nil || result.Status != "not_required" {
		t.Fatal("plaintext protocol incorrectly reported as certificate verified")
	}
}
