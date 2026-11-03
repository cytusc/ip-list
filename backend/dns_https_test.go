package main

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestWebsiteHTTPSHealth(t *testing.T) {
	c := dnsTestConfig()
	c.WebsiteEnabled, c.WebsiteURL = true, "https://api.example.com/health"
	for _, tc := range []struct {
		name     string
		outcomes map[string][]bool
		want     map[string]string
	}{
		{"confirmed failures", map[string][]bool{"1.1.1.1": {true, true}, "8.8.8.8": {false, false}}, map[string]string{"1.1.1.1": "healthy", "8.8.8.8": "failed"}},
		{"brief failure", map[string][]bool{"1.1.1.1": {true, true}, "8.8.8.8": {false, true}}, map[string]string{"1.1.1.1": "healthy", "8.8.8.8": "unknown"}},
		{"checker outage", map[string][]bool{"1.1.1.1": {false, false}, "8.8.8.8": {false, false}}, map[string]string{"1.1.1.1": "unknown", "8.8.8.8": "unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := []dnsCandidate{{IP: "1.1.1.1", Pool: "cf", State: "healthy"}, {IP: "8.8.8.8", Pool: "cf", State: "healthy"}, {IP: "9.9.9.9", State: "healthy"}}
			var mu sync.Mutex
			calls := map[string]int{}
			check := func(ctx context.Context, ip, target string) error {
				mu.Lock()
				defer mu.Unlock()
				if target != c.WebsiteURL {
					t.Errorf("lost business URL: %s", target)
				}
				ok := tc.outcomes[ip][calls[ip]]
				calls[ip]++
				if !ok {
					return errors.New("TLS certificate or connection failed")
				}
				return nil
			}
			dnsCheckWebsiteWith(context.Background(), c, items, check)
			for _, item := range items[:2] {
				if item.HTTPSState != tc.want[item.IP] || item.State != tc.want[item.IP] || item.HTTPSURL != c.WebsiteURL || calls[item.IP] != 2 {
					t.Fatalf("%+v calls=%v", items, calls)
				}
			}
			if items[2].HTTPSState != "" || calls["9.9.9.9"] != 0 {
				t.Fatal("modified ordinary protocol pool")
			}
		})
	}
	items := []dnsCandidate{{IP: "1.1.1.1", Pool: "cf", State: "healthy"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dnsCheckWebsiteWith(ctx, c, items, func(context.Context, string, string) error { t.Fatal("called after cancellation"); return nil })
	if items[0].State != "unknown" {
		t.Fatal(items)
	}
}

func TestWebsiteHTTPSPlanIsolation(t *testing.T) {
	c := dnsTestConfig()
	c.WebsiteEnabled, c.WebsiteURL = true, "https://api.example.com/"
	remote := []dnsRemoteRecord{{ID: "url", Name: "url.reiiv.com", Type: "A", Line: "default_view", Status: "ACTIVE", TTL: 300, Values: []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"}}}
	items := []dnsCandidate{
		{IP: "1.1.1.1", Pool: "cf", State: "healthy", Country: "HK", Delay: 10, HTTPSState: "healthy", HTTPSURL: c.WebsiteURL},
		{IP: "8.8.8.8", Pool: "cf", State: "failed", HTTPSState: "failed", HTTPSURL: c.WebsiteURL},
		{IP: "9.9.9.9", Pool: "cf", State: "unknown"},
		{IP: "1.0.0.1", Pool: "cf", State: "healthy", Country: "HK", Delay: 5, HTTPSState: "healthy", HTTPSURL: c.WebsiteURL},
	}
	for _, outage := range []bool{false, true} {
		plan := dnsBuildPlan(c, items, remote, map[string]bool{"url.reiiv.com/A": true}, outage)
		want := []string{"1.0.0.1", "1.1.1.1", "9.9.9.9"}
		if outage {
			want = []string{"1.1.1.1", "9.9.9.9"}
		}
		if len(plan) != 1 || plan[0].Action != "update" || !slices.Equal(plan[0].Desired, want) {
			t.Fatalf("outage=%v plan=%+v", outage, plan)
		}
	}
	// Conflicting or inactive cloud records must still never be overwritten.
	remote[0].Status = "DISABLE"
	if p := dnsBuildPlan(c, items, remote, nil, true); p[0].Action != "hold" {
		t.Fatal(p)
	}
	remote[0].Status = "ACTIVE"
	remote = append(remote, remote[0])
	if p := dnsBuildPlan(c, items, remote, nil, true); p[0].Action != "hold" {
		t.Fatal(p)
	}
}

func TestWebsiteHTTPSRecovery(t *testing.T) {
	c := dnsTestConfig()
	c.WebsiteEnabled, c.WebsiteURL = true, "https://api.example.com/"
	now := time.Now()
	item := dnsCandidate{IP: "1.1.1.1", Pool: "cf", State: "pending", Country: "HK", Delay: 20, HTTPSState: "healthy", HTTPSURL: c.WebsiteURL}
	history := []dnsReport{}
	for _, age := range []int{5, 10, 15} {
		history = append(history, dnsReport{Mode: "sync", StartedAt: now.Add(-time.Duration(age) * time.Minute), Config: c, Candidates: []dnsCandidate{item}, Warning: "another IP location unavailable"})
	}
	if !dnsWebsiteStable(c, item, history, now) {
		t.Fatal("unrelated lookup failure blocked healthy candidate")
	}
	history[1].Candidates[0].HTTPSState = "unknown"
	if dnsWebsiteStable(c, item, history, now) {
		t.Fatal("admitted intermittent HTTPS")
	}
	history[1].Candidates[0].HTTPSState = "healthy"
	history[1].Config.WebsiteURL = "https://old.example/"
	if dnsWebsiteStable(c, item, history, now) {
		t.Fatal("reused another business domain's history")
	}
}

func TestWebsiteHTTPSConfig(t *testing.T) {
	for _, target := range []string{"", "http://api.example.com/", "https://user:secret@api.example.com/", "https://api.example.com/#part"} {
		c := dnsTestConfig()
		c.WebsiteEnabled, c.WebsiteURL = true, target
		if c.validate() == nil {
			t.Fatalf("accepted %q", target)
		}
	}
	c := dnsTestConfig()
	c.WebsiteEnabled, c.WebsiteURL = true, "https://api.example.com/health"
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
}
