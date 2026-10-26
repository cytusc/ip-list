package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
