package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type locationRoundTrip func(*http.Request) (*http.Response, error)

func (f locationRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIPLocationDirectFallback(t *testing.T) {
	for _, tc := range []struct {
		name            string
		info, db, who   string
		country, status string
		wantErr         bool
	}{
		{"Hong Kong consensus", `{"ip":"47.75.104.211","country":"HK","city":"Hong Kong"}`, `{"ipAddress":"47.75.104.211","countryCode":"HK","stateProv":"Kowloon","city":"Hong Kong"}`, `{"ip":"47.75.104.211","country_code":"HK","success":true}`, "HK", "consensus", false},
		{"Singapore majority", `{"ip":"47.75.104.211","country":"SG","city":"Singapore"}`, `{"ipAddress":"47.75.104.211","countryCode":"US","city":"Bozeman"}`, `{"ip":"47.75.104.211","country_code":"SG","success":true}`, "SG", "consensus", false},
		{"conflict", `{"ip":"47.75.104.211","country":"HK"}`, `{"ipAddress":"47.75.104.211","countryCode":"US"}`, `{"ip":"47.75.104.211","country_code":"SG","success":true}`, "", "conflict", false},
		{"single source", `{"ip":"47.75.104.211","country":"HK"}`, `{"errorCode":"TOO_MANY_REQUESTS"}`, `{"ip":"47.75.104.211","country_code":"HK","success":false}`, "", "insufficient", false},
		{"wrong address ignored", `{"ip":"8.8.8.8","country":"US"}`, `{"ipAddress":"47.75.104.211","countryCode":"HK"}`, `{"ip":"47.75.104.211","country_code":"HK","success":true}`, "HK", "consensus", false},
		{"all unavailable", `not JSON`, `{"errorCode":"TOO_MANY_REQUESTS"}`, `{"success":false}`, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: locationRoundTrip(func(r *http.Request) (*http.Response, error) {
				body := map[string]string{"ipinfo.io": tc.info, "api.db-ip.com": tc.db, "ipwho.is": tc.who}[r.URL.Host]
				if body == "" {
					return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}
			got, err := lookupReliableIPLocation(context.Background(), client, "https://primary.example", "47.75.104.211")
			if (err != nil) != tc.wantErr || got.Country != tc.country || got.Status != tc.status {
				t.Fatalf("got=%+v err=%v", got, err)
			}
			if got.Country == "SG" && got.City == "Bozeman" {
				t.Fatal("mixed losing country's city")
			}
		})
	}
}

func TestIPLocationPrimaryConflictDoesNotUseFallback(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: locationRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Host != "primary.example" {
			t.Error("fallback overruled valid conflict")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"data":{"ip":"47.75.104.211","geo":{"MaxMind":{"country_code":"SG"},"DB-IP":{"country_code":"US"}}}}`))}, nil
	})}
	got, err := lookupReliableIPLocation(context.Background(), client, "https://primary.example", "47.75.104.211")
	if err != nil || got.Status != "conflict" || calls.Load() != 1 {
		t.Fatal(got, err, calls.Load())
	}
}

func TestIPLocationDirectSlowSource(t *testing.T) {
	client := &http.Client{Transport: locationRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.db-ip.com" {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ip":"47.75.104.211","country":"HK","country_code":"HK","success":true}`))}, nil
	})}
	got, err := lookupDirectIPLocation(context.Background(), client, "47.75.104.211")
	if err != nil || got.Country != "HK" || got.Status != "consensus" {
		t.Fatal(got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = lookupDirectIPLocation(ctx, client, "47.75.104.211")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
