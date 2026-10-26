package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPinnedConnectionAndAuthentication(t *testing.T) {
	received := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received++
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(settings{Interval: 60, IPs: []string{"127.0.0.1"}})
	}))
	defer server.Close()
	digest := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	correct := base64.StdEncoding.EncodeToString(digest[:])
	for _, tc := range []struct {
		name, pin, token string
		wantOK           bool
	}{
		{"trusted public key", correct, "test-secret", true},
		{"wrong key blocked before authentication", base64.StdEncoding.EncodeToString(make([]byte, 32)), "test-secret", false},
		{"untrusted CA rejected without pin", "", "test-secret", false},
		{"invalid token", correct, "wrong-secret", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := newHTTPClient(tc.pin)
			if err != nil {
				t.Fatal(err)
			}
			before := received
			_, err = loadSettings(client, server.URL, tc.token)
			if (err == nil) != tc.wantOK {
				t.Fatalf("unexpected result: %v", err)
			}
			if strings.Contains(tc.name, "blocked") && received != before {
				t.Fatal("sent credentials before pin validation")
			}
		})
	}
	if _, err := newHTTPClient("invalid!"); err == nil {
		t.Fatal("invalid fingerprint accepted")
	}
}

func TestAgentDoesNotFollowRedirects(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	client, err := newHTTPClient("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadSettings(client, source.URL, "secret"); err == nil {
		t.Fatal("redirect accepted")
	}
	if redirected {
		t.Fatal("agent followed redirect")
	}
}

func TestMeasurementInterfaceRejectsTunnelFallback(t *testing.T) {
	physical := net.Interface{Name: "en0", Flags: net.FlagUp | net.FlagBroadcast, HardwareAddr: net.HardwareAddr{0, 1, 2, 3, 4, 5}}
	tunnel := net.Interface{Name: "utun4", Flags: net.FlagUp | net.FlagPointToPoint}
	virtualEthernet := net.Interface{Name: "Clash", Flags: net.FlagUp | net.FlagBroadcast, HardwareAddr: physical.HardwareAddr}
	wifi := physical
	wifi.Name = "en1"
	down := physical
	down.Flags = 0
	for _, tc := range []struct {
		name       string
		interfaces []net.Interface
		configured string
		want       string
	}{
		{"TUN default must not affect physical selection", []net.Interface{tunnel, physical, virtualEthernet}, "", "en0"},
		{"explicit tunnel rejected", []net.Interface{physical, tunnel}, "utun4", ""},
		{"virtual Ethernet rejected", []net.Interface{virtualEthernet, physical}, "Clash", ""},
		{"no physical must not fall back", []net.Interface{tunnel}, "", ""},
		{"disconnected must not fall back", []net.Interface{tunnel, down}, "", ""},
		{"multiple physical require selection", []net.Interface{physical, wifi}, "", ""},
		{"explicit physical with multiple candidates", []net.Interface{physical, wifi}, "en1", "en1"},
		{"missing configured interface must not fall back", []net.Interface{physical}, "missing", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectMeasurementInterface(tc.interfaces, tc.configured, func(net.Interface, bool) string { return "192.168.2.96" })
			if tc.want == "" {
				if err == nil {
					t.Fatalf("unsafe interface accepted: %s", got.Name)
				}
				return
			}
			if err != nil || got.Name != tc.want {
				t.Fatalf("got %s, %v", got.Name, err)
			}
		})
	}
	if _, err := selectMeasurementInterface([]net.Interface{physical}, "", func(net.Interface, bool) string { return "" }); err == nil {
		t.Fatal("interface without a usable address accepted")
	}
}

func TestPingBindsSelectedInterface(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows binds its source address before execution")
	}
	for _, raw := range []string{"1.1.1.1", "2606:4700:4700::1111"} {
		args := pingArgs(netip.MustParseAddr(raw), time.Second, "en0")
		flag := "-I"
		if runtime.GOOS == "darwin" {
			flag = "-b"
		}
		index := slices.Index(args, flag)
		if index < 0 || index+1 >= len(args) || args[index+1] != "en0" {
			t.Fatalf("ping does not bind selected interface: %v", args)
		}
	}
}
