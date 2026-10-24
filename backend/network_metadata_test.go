package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestNetworkMetadataLookup(t *testing.T) {
	for _, tc := range []struct {
		name, network, holder string
		want                  bool
	}{
		{"single", `{"status":"ok","data":{"asns":["36002"],"prefix":"103.26.8.0/24"}}`, `{"status":"ok","data":{"holder":"Example Networks"}}`, true},
		{"multiple", `{"status":"ok","data":{"asns":["36002","13335"],"prefix":"103.26.8.0/24"}}`, "", false},
		{"partial", `{"status":"ok","data":{"asns":["36002"],"prefix":"103.26.8.0/25"}}`, "", false},
		{"unrelated", `{"status":"ok","data":{"asns":["36002"],"prefix":"103.26.9.0/24"}}`, "", false},
		{"unannounced", `{"status":"ok","data":{"asns":[],"prefix":""}}`, "", false},
		{"missing-holder", `{"status":"ok","data":{"asns":["36002"],"prefix":"103.26.8.0/24"}}`, `{"status":"ok","data":{"holder":""}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/data/network-info/data.json" {
					if r.URL.Query().Get("resource") != "103.26.8.0" {
						t.Error("wrong resource")
					}
					fmt.Fprint(w, tc.network)
				} else {
					if r.URL.Query().Get("resource") != "AS36002" {
						t.Error("wrong ASN")
					}
					fmt.Fprint(w, tc.holder)
				}
			}))
			defer server.Close()
			got, err := lookupNetworkMetadata(context.Background(), server.Client(), server.URL, netip.MustParsePrefix("103.26.8.0/24"))
			if (err == nil) != tc.want {
				t.Fatalf("unexpected outcome: %+v %v", got, err)
			}
			if tc.want && (got.ASN != "AS36002" || got.Provider != "Example Networks") {
				t.Fatal(got)
			}
		})
	}
}

func TestNetworkMetadataFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }))
	defer server.Close()
	prefix := netip.MustParsePrefix("103.26.8.0/24")
	if _, err := lookupNetworkMetadata(context.Background(), server.Client(), server.URL, prefix); err == nil {
		t.Fatal("accepted rate limit response")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if _, err := lookupNetworkMetadata(ctx, server.Client(), server.URL, prefix); err == nil {
		t.Fatal("ignored deadline")
	}
	for _, cidr := range []string{"127.0.0.1/32", "192.168.0.0/24", "2001:db8::/32", "192.0.2.0/24"} {
		if publicMetadataPrefix(netip.MustParsePrefix(cidr)) {
			t.Fatal("queried nonpublic prefix", cidr)
		}
	}
}
