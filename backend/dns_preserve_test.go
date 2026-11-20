package main

import (
	"slices"
	"testing"
)

func TestDNSPreservesEachRecordWithoutReplacement(t *testing.T) {
	c := dnsTestConfig()
	c.WebsiteEnabled = true
	for _, state := range []string{"missing", "pending", "unknown", "failed"} {
		t.Run(state, func(t *testing.T) {
			remote := []dnsRemoteRecord{
				{ID: "hk4", Name: "hk.reiiv.com", Type: "A", Line: "default_view", Status: "ACTIVE", TTL: 300, Values: []string{"103.135.251.114", "191.222.220.126"}},
				{ID: "hk6", Name: "hk.reiiv.com", Type: "AAAA", Line: "default_view", Status: "ACTIVE", TTL: 300, Values: []string{"2606:4700:4700::1111"}},
			}
			candidates := []dnsCandidate{{IP: "1.1.1.1", Country: "HK", Pool: "cf", State: "healthy"}, {IP: "8.8.8.8", Country: "US", State: "healthy"}}
			if state != "missing" {
				candidates = append(candidates, dnsCandidate{IP: remote[0].Values[0], Country: "HK", State: state})
			}
			managed := map[string]bool{"hk.reiiv.com/A": true, "hk.reiiv.com/AAAA": true}
			websiteUpdated := false
			for _, r := range dnsBuildPlan(c, candidates, remote, managed, false) {
				if r.Name == "url.reiiv.com" && r.Action == "create" {
					websiteUpdated = true
				}
				if r.Name == "hk.reiiv.com" && (r.Action != "hold" || !slices.Equal(r.Current, r.Desired)) {
					t.Fatalf("lost original record with %s candidates: %+v", state, r)
				}
			}
			if !websiteUpdated {
				t.Fatal("fixture must include a healthy website record")
			}
			// A healthy IPv4 replacement must not unlock deletion of IPv6.
			candidates = append(candidates, dnsCandidate{IP: "47.243.169.208", Country: "HK", State: "healthy"})
			for _, r := range dnsBuildPlan(c, candidates, remote, managed, false) {
				if r.Name != "hk.reiiv.com" {
					continue
				}
				if r.Type == "A" && (r.Action != "update" || !slices.Equal(r.Desired, []string{"47.243.169.208"})) || r.Type == "AAAA" && (r.Action != "hold" || !slices.Equal(r.Current, r.Desired)) {
					t.Fatalf("record families affected each other: %+v", r)
				}
			}
		})
	}
}
