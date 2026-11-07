package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIPLocationSignedLookup(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		wantError     bool
	}{
		{"location", `{"ok":true,"data":{"ip":"8.8.8.8","geo":{"IP2Location":{"country_code":"us","subdivisions":"California","city":"Mountain View"},"DB-IP":{"country_code":"US","city":"Other city"},"MaxMind":{"country_code":"US","city":""}}}}`, false},
		{"DB-IP consensus", `{"ok":true,"data":{"ip":"8.8.8.8","geo":{"IP2Location":{"country_code":"??"},"DB-IP":{"country_code":"US","subdivisions":"California","city":"Mountain View"},"MaxMind":{"country_code":"US"}}}}`, false},
		{"MaxMind consensus", `{"ok":true,"data":{"ip":"8.8.8.8","geo":{"MaxMind":{"country_code":"US","subdivisions":"California","city":"Mountain View"},"IPInfo":{"country_code":"US"}}}}`, false},
		{"wrong IP", `{"ok":true,"data":{"ip":"1.1.1.1","geo":{"IP2Location":{"country_code":"US"}}}}`, true},
		{"upstream failure", `{"ok":false}`, true},
		{"invalid country", `{"ok":true,"data":{"ip":"8.8.8.8","geo":{"IP2Location":{"country_code":"??"}}}}`, true},
		{"missing geo", `{"ok":true,"data":{"ip":"8.8.8.8"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/info/ip-basic/8.8.8.8" || r.Header.Get("Origin") != "https://ippure.com" {
					t.Errorf("unexpected request: %s", r.URL)
				}
				if r.Header.Get("x-k") == "" {
					w.Header().Set("x-k", "test-key")
					w.Header().Set("x-t", fmt.Sprint(time.Now().Add(time.Hour).UnixMilli()))
					w.WriteHeader(http.StatusForbidden)
					return
				}
				stamp, signature, ok := strings.Cut(r.Header.Get("x-t"), "-")
				mac := hmac.New(sha256.New, []byte("test-key"))
				fmt.Fprintf(mac, "GET-http://%s%s--%s", r.Host, r.URL, stamp)
				if !ok || r.Header.Get("x-k") != "test-key" || signature != hex.EncodeToString(mac.Sum(nil)) {
					t.Error("invalid signature")
				}
				fmt.Fprint(w, tc.payload)
			}))
			defer server.Close()
			location, err := lookupIPLocation(context.Background(), server.Client(), server.URL, "8.8.8.8")
			if (err != nil) != tc.wantError || calls != 2 {
				t.Fatalf("location=%+v error=%v calls=%d", location, err, calls)
			}
			if !tc.wantError && (location.Country != "US" || location.Region != "California" || location.City != "Mountain View") {
				t.Fatalf("unexpected location: %+v", location)
			}
		})
	}
}

func TestIPLocationConsensus(t *testing.T) {
	for _, tc := range []struct{ name, payload, country, status string }{
		{"reported Singapore conflict", `{"IP2Location":{"country_code":"US","city":"Bozeman"},"DB-IP":{"country_code":"SG","city":"Singapore"},"MaxMind":{"country_code":"SG","city":"Singapore"},"IPInfo.io":{"country_code":"SG"}}`, "SG", "consensus"},
		{"two of three", `{"IP2Location":{"country_code":"US"},"DB-IP":{"country_code":"SG"},"MaxMind":{"country_code":"SG"}}`, "SG", "consensus"},
		{"tie", `{"IP2Location":{"country_code":"US"},"DB-IP":{"country_code":"SG"}}`, "", "conflict"},
		{"plurality is not majority", `{"IP2Location":{"country_code":"US"},"DB-IP":{"country_code":"SG"},"MaxMind":{"country_code":"SG"},"IPInfo":{"country_code":"JP"}}`, "", "conflict"},
		{"three different", `{"IP2Location":{"country_code":"US"},"DB-IP":{"country_code":"SG"},"MaxMind":{"country_code":"JP"}}`, "", "conflict"},
		{"single source", `{"IP2Location":{"country_code":"US"}}`, "", "insufficient"},
		{"invalid country ignored", `{"IP2Location":{"country_code":"ZZ"},"DB-IP":{"country_code":" sg "},"MaxMind":{"country_code":"SG"}}`, "SG", "consensus"},
		{"IPInfo aliases count once", `{"IPInfo":{"country_code":"SG"},"IPInfo.io":{"country_code":"SG"},"IP2Location":{"country_code":"US"},"Bilibili":{"country_code":"UA"}}`, "", "conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var geo map[string]ipGeoRecord
			if err := json.Unmarshal([]byte(tc.payload), &geo); err != nil {
				t.Fatal(err)
			}
			location, err := consensusIPLocation(geo)
			if err != nil || location.Country != tc.country || location.Status != tc.status {
				t.Fatalf("location=%+v err=%v", location, err)
			}
			if location.Country == "SG" && location.City == "Bozeman" {
				t.Fatal("mixed geography from losing country")
			}
		})
	}
}

func TestIPLocationManualOverride(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	ip := "178.94.15.179"
	a.cacheListCountry(ctx, ip, ipLocation{Country: "SG", City: "Singapore", Status: "consensus", Source: "MaxMind"})
	_, err := a.db.Exec(ctx, `INSERT INTO nodes(name,address,port,share_uri) VALUES('location','host',443,'vless://location@host:443?type=grpc&security=tls');
 INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at,config_hash,certificate) VALUES('grpc','178.94.15.179',true,65,now(),md5('vless://location@host:443?type=grpc&security=tls'),'{"status":"passed"}');`)
	if err != nil {
		t.Fatal(err)
	}
	verifyPoolFixture(t, a.db)
	checkFilter := func(want string) {
		t.Helper()
		var country string
		if err := a.db.QueryRow(ctx, availableIPCandidatesSQL+`SELECT country FROM candidates WHERE ip=$5::inet`, "", "", "", "", ip).Scan(&country); err != nil || country != want {
			t.Fatalf("filter country=%q want=%q err=%v", country, want, err)
		}
	}
	checkFilter("SG")
	if _, err = a.db.Exec(ctx, "UPDATE dns_ip_locations SET policy_version=0 WHERE ip=$1::inet", ip); err != nil {
		t.Fatal(err)
	}
	checkFilter("")
	a.cacheListCountry(ctx, ip, ipLocation{Country: "SG", City: "Singapore", Status: "consensus"})
	call := func(method, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/available-ips/location?ip="+ip, strings.NewReader(body))
		w := httptest.NewRecorder()
		r.Header.Set("Content-Type", "application/json")
		switch method {
		case "PUT":
			a.saveIPLocationOverride(w, r)
		case "DELETE":
			a.deleteIPLocationOverride(w, r)
		default:
			a.availableIPLocation(w, r)
		}
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
		return w
	}
	call("PUT", `{"country":"XX"}`, 422)
	call("PUT", `{"country":"us","region":"Montana","city":"Bozeman"}`, 200)
	checkFilter("US")
	if country, err := a.dnsLocation(ctx, ip); err != nil || country != "US" {
		t.Fatal(country, err)
	}
	// Cache refreshes and repeatable schema migrations preserve manual choices.
	a.cacheListCountry(ctx, ip, ipLocation{Country: "SG", City: "Singapore", Status: "consensus"})
	checkFilter("US")
	if _, err := a.db.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	w := call("GET", "", 200)
	var location ipLocation
	if err := json.Unmarshal(w.Body.Bytes(), &location); err != nil || location.Status != "manual" || location.Country != "US" || location.City != "Bozeman" {
		t.Fatal(w.Body.String(), err)
	}
	w = call("DELETE", "", 200)
	checkFilter("SG")
	if err := json.Unmarshal(w.Body.Bytes(), &location); err != nil || location.Status != "consensus" || location.Country != "SG" {
		t.Fatal(w.Body.String(), err)
	}
	if country, err := a.dnsLocation(ctx, ip); err != nil || country != "SG" {
		t.Fatal(country, err)
	}
	a.cacheListCountry(ctx, ip, ipLocation{Status: "conflict"})
	if _, err := a.dnsLocation(ctx, ip); !errors.Is(err, errLocationConflict) {
		t.Fatal(err)
	}
	// Conflict must preserve the published address without holding unrelated work.
	plan := dnsBuildPlan(dnsTestConfig(), []dnsCandidate{{IP: ip, State: "unknown"}, {IP: "1.1.1.1", Country: "HK", State: "healthy"}}, []dnsRemoteRecord{{Name: "us.reiiv.com", Type: "A", Line: "default_view", Status: "ACTIVE", Values: []string{ip}}}, nil, false)
	for _, record := range plan {
		if record.Action == "hold" {
			t.Fatal(plan)
		}
		if record.Name == "us.reiiv.com" && (len(record.Desired) != 1 || record.Desired[0] != ip) {
			t.Fatal(plan)
		}
	}
}
