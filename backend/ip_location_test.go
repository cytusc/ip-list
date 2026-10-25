package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
		{"location", `{"ok":true,"data":{"ip":"8.8.8.8","country":"us","region":"California","city":"Mountain View"}}`, false},
		{"wrong IP", `{"ok":true,"data":{"ip":"1.1.1.1","country":"US"}}`, true},
		{"upstream failure", `{"ok":false}`, true},
		{"invalid country", `{"ok":true,"data":{"ip":"8.8.8.8","country":"??"}}`, true},
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
