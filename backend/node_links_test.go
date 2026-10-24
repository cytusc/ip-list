package main

import (
	"net/http/httptest"
	"net/netip"
	"net/url"
	"reflect"
	"testing"
)

func TestNodeLinkPreservesConfiguration(t *testing.T) {
	for _, transport := range []string{"httpupgrade", "xhttp", "grpc", "ws", "tcp"} {
		for _, ip := range []string{"192.0.2.7", "2001:db8::7"} {
			for _, explicit := range []bool{false, true} {
				raw := "vless://" + testUUID + "@original.example:443?encryption=none&security=tls&type=" + transport + "&path=%2Ffoo%3Fbar%3D1&serviceName=service&alpn=h2&fp=chrome"
				if explicit {
					raw += "&sni=tls.example&host=tls.example"
				}
				before, _, err := parseShare(raw)
				if err != nil {
					t.Fatal(err)
				}
				generated, err := nodeLink(raw, "香港 # 节点", netip.MustParseAddr(ip))
				if err != nil {
					t.Fatal(err)
				}
				u, err := url.Parse(generated)
				if err != nil {
					t.Fatal(err)
				}
				if u.Hostname() != ip || u.Port() != "443" || u.User.Username() != testUUID || u.Fragment != "香港 # 节点 · "+ip {
					t.Fatal("endpoint or identity changed")
				}
				after, _, err := parseShare(generated)
				if err != nil {
					t.Fatal(err)
				}
				before["server"] = ip
				// The converter chooses a random browser User-Agent for WS.
				for _, config := range []map[string]any{before, after} {
					if opts, ok := config["ws-opts"].(map[string]any); ok {
						if headers, ok := opts["headers"].(map[string]any); ok {
							delete(headers, "User-Agent")
						}
					}
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("%s explicit=%v: generated link changes transport parameters", transport, explicit)
				}
			}
		}
	}
}
func TestGenerateLinksRejectsInvalidIP(t *testing.T) {
	for _, ip := range []string{"invalid", "fe80::1%en0", "192.0.2.1/24"} {
		w := httptest.NewRecorder()
		(&app{}).generateIPLinks(w, httptest.NewRequest("GET", "/available-ips/links?ip="+url.QueryEscape(ip), nil))
		if w.Code != 422 {
			t.Fatalf("invalid IP accepted: %s", ip)
		}
	}
}
func TestNetworkASNValidation(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		valid    bool
	}{{"", "", true}, {"13335", "AS13335", true}, {" as013335 ", "AS13335", true}, {"AS4294967295", "AS4294967295", true}, {"AS0", "", false}, {"AS4294967296", "", false}, {"ASfoo", "", false}, {"+13335", "", false}} {
		in := ConfigInput{Name: "test", CIDR: "192.0.2.0/24", ASN: tc.in, Provider: " Cloudflare "}
		fields := in.validate("networks")
		if (fields["asn"] == "") != tc.valid || tc.valid && in.ASN != tc.want || in.Provider != "Cloudflare" {
			t.Fatalf("%s: %+v", tc.in, fields)
		}
	}
}
