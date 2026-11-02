package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestNetworkMetadataLookup(t *testing.T) {
	for _, tc := range []struct {
		name, network, holder string
		want                  bool
	}{
		{"single", `{"status":"ok","data":{"asns":["36002"],"prefix":"103.26.8.0/24"}}`, `{"status":"ok","data":{"holder":"Example Networks"}}`, true},
		{"multiple", `{"status":"ok","data":{"asns":["36002","13335"],"prefix":"103.26.8.0/24"}}`, `{"status":"ok","data":{"holder":"Example Networks"}}`, true},
		{"partial", `{"status":"ok","data":{"asns":["36002"],"prefix":"103.26.8.0/25"}}`, "", false},
		{"unrelated", `{"status":"ok","data":{"asns":["36002"],"prefix":"103.26.9.0/24"}}`, "", false},
		{"unannounced", `{"status":"ok","data":{"asns":[],"prefix":""}}`, "", false},
		{"missing-holder", `{"status":"ok","data":{"asns":["36002"],"prefix":"103.26.8.0/24"}}`, `{"status":"ok","data":{"holder":""}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/data/network-info/data.json" {
					// Partial routes require querying the uncovered half as well.
					if tc.name == "partial" && r.URL.Query().Get("resource") == "103.26.8.128" {
						fmt.Fprint(w, `{"status":"ok","data":{"asns":[],"prefix":""}}`)
						return
					}
					if r.URL.Query().Get("resource") != "103.26.8.0" {
						t.Error("wrong resource")
					}
					fmt.Fprint(w, tc.network)
				} else {
					if r.URL.Query().Get("resource") != "AS36002" && !(tc.name == "multiple" && r.URL.Query().Get("resource") == "AS13335") {
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
			wantASN := "AS36002"
			if tc.name == "multiple" {
				wantASN = "AS13335, AS36002"
			}
			if tc.want && (got.ASN != wantASN || got.Provider != "Example Networks") {
				t.Fatal(got)
			}
		})
	}
}

func TestNormalizeASNs(t *testing.T) {
	for _, raw := range []string{"AS32519, 906,AS906", "as906, as32519"} {
		got, err := normalizeASNs(raw)
		if err != nil || got != "AS906, AS32519" {
			t.Fatalf("%q: %q %v", raw, got, err)
		}
	}
	for _, raw := range []string{"", "AS0", "AS4294967296", "AS906,", "AS906 / AS32519", strings.Repeat("AS4294967295,", 30)} {
		if _, err := normalizeASNs(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestMetadataRetryBackoff(t *testing.T) {
	now := time.Now()
	var retry metadataRetry
	for _, want := range []time.Duration{30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 6 * time.Hour, 6 * time.Hour} {
		retry = nextMetadataRetry(retry, now)
		if retry.delay != want || !retry.next.Equal(now.Add(want)) {
			t.Fatalf("got %+v, want %v", retry, want)
		}
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

func TestNetworkRegistration(t *testing.T) {
	for _, tc := range []struct {
		name, records, country, holder string
		want                           bool
	}{
		{"allocation", `[[{"key":"inetnum","value":"152.175.0.0/16"},{"key":"country","value":"jp"},{"key":"descr","value":"SAKURA LINK LIMITED"}]]`, "JP", "SAKURA LINK LIMITED", true},
		{"range", `[[{"key":"NetRange","value":"152.175.0.0 - 152.175.255.255"},{"key":"Country","value":"JP"},{"key":"OrgName","value":"Example"}]]`, "JP", "Example", true},
		{"most-specific", `[[{"key":"inetnum","value":"152.0.0.0/8"},{"key":"country","value":"US"},{"key":"descr","value":"Parent"}],[{"key":"inetnum","value":"152.175.0.0/16"},{"key":"country","value":"JP"},{"key":"descr","value":"Child"}]]`, "JP", "Child", true},
		{"unrelated", `[[{"key":"inetnum","value":"152.174.0.0/16"},{"key":"country","value":"JP"}]]`, "", "", false},
		{"partial", `[[{"key":"inetnum","value":"152.175.0.0/24"},{"key":"country","value":"JP"}]]`, "", "", false},
		{"contact-only", `[[{"key":"country","value":"JP"},{"key":"descr","value":"Contact address"}]]`, "", "", false},
		{"invalid-country", `[[{"key":"inetnum","value":"152.175.0.0/16"},{"key":"country","value":"JAPAN"},{"key":"netname","value":"Example"}]]`, "", "Example", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("resource") != "152.175.0.0/16" {
					t.Error("must query the whole prefix")
				}
				fmt.Fprintf(w, `{"status":"ok","data":{"records":%s}}`, tc.records)
			}))
			defer server.Close()
			got, err := lookupNetworkRegistration(context.Background(), server.Client(), server.URL, netip.MustParsePrefix("152.175.0.0/16"))
			if (err == nil) != tc.want || got.Country != tc.country || got.Registry != tc.holder || got.ASN != "" || got.Provider != "" {
				t.Fatalf("registration must not be presented as an operator: %+v %v", got, err)
			}
		})
	}
}

func TestCompleteNetworkMetadataPreservesIndependentResults(t *testing.T) {
	for _, mode := range []string{"routing-fails", "routing-timeout", "whois-fails", "both-succeed", "both-fail"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/data/whois/data.json" {
					if mode == "whois-fails" || mode == "both-fail" {
						w.WriteHeader(503)
						return
					}
					fmt.Fprint(w, `{"status":"ok","data":{"records":[[{"key":"inetnum","value":"152.175.0.0/16"},{"key":"country","value":"JP"},{"key":"descr","value":"Registered holder"}]]}}`)
					return
				}
				if mode == "routing-timeout" {
					<-r.Context().Done()
					return
				}
				if mode == "routing-fails" || mode == "both-fail" {
					w.WriteHeader(503)
					return
				}
				if r.URL.Path == "/data/network-info/data.json" {
					fmt.Fprint(w, `{"status":"ok","data":{"asns":["202069"],"prefix":"152.175.0.0/16"}}`)
				} else {
					fmt.Fprint(w, `{"status":"ok","data":{"holder":"Actual operator"}}`)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got, err := lookupCompleteNetworkMetadata(ctx, server.Client(), server.URL, netip.MustParsePrefix("152.175.0.0/16"))
			if mode == "both-fail" {
				if err == nil {
					t.Fatal("missing failure")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode != "whois-fails" && (got.Country != "JP" || got.Registry != "Registered holder") {
				t.Fatal(got)
			}
			if mode == "routing-fails" || mode == "routing-timeout" {
				if got.ASN != "" || got.Provider != "" {
					t.Fatal("must not use the registrant as the operator", got)
				}
			} else if got.ASN != "AS202069" || got.Provider != "Actual operator" {
				t.Fatal(got)
			}
		})
	}
}
