package main

import (
	"math"
	"slices"
	"strings"
	"testing"
)

func TestDNSSingaporeFallback(t *testing.T) {
	c := dnsTestConfig()
	hk := []dnsCandidate{
		{IP: "47.243.169.208", Country: "HK", State: "healthy", Delay: 40},
		{IP: "47.243.238.144", Country: "HK", State: "healthy", Delay: 10},
		{IP: "47.75.104.211", Country: "HK", State: "healthy", Delay: 30},
		{IP: "47.75.104.254", Country: "HK", State: "healthy", Delay: 20},
		{IP: "47.75.104.254", Country: "HK", State: "healthy", Delay: 20},
		{IP: "1.1.1.1", Country: "HK", State: "failed", Delay: 1},
		{IP: "8.8.8.8", Country: "HK", State: "unknown", Delay: 1},
		{IP: "9.9.9.9", Country: "HK", State: "healthy", Delay: math.NaN()},
		{IP: "103.26.8.38", Country: "SG", State: "failed"},
		{IP: "23.128.188.62", Country: "SG", State: "unknown"},
		{IP: "141.11.77.154", Country: "HK", State: "healthy", Delay: 1, Pool: "cf"},
	}
	key := "sg.reiiv.com/A"
	desired := dnsDesired(c, hk)
	want := []string{"47.243.238.144", "47.75.104.211", "47.75.104.254"}
	if !slices.Equal(desired[key].Desired, want) || !strings.Contains(desired[key].Reason, "兜底") || desired[key].Country != "SG" {
		t.Fatalf("fallback: %+v", desired[key])
	}
	c.MaxIPs = 1
	if got := dnsDesired(c, hk)[key].Desired; !slices.Equal(got, want[:1]) {
		t.Fatalf("configured cap: %v", got)
	}
	c.MaxIPs = 10
	recovered := append(slices.Clone(hk), dnsCandidate{IP: "103.26.8.38", Country: "SG", State: "healthy", Delay: 100})
	if got := dnsDesired(c, recovered)[key]; !slices.Equal(got.Desired, []string{"103.26.8.38"}) || got.Reason != "" {
		t.Fatalf("recovery: %+v", got)
	}
	if _, ok := dnsDesired(c, hk[len(hk)-1:])[key]; ok {
		t.Fatal("website-only pool leaked into country fallback")
	}
	ipv6 := append(slices.Clone(recovered), dnsCandidate{IP: "2606:4700:4700::1111", Country: "HK", State: "healthy", Delay: 20})
	v6 := dnsDesired(c, ipv6)
	if !slices.Equal(v6["sg.reiiv.com/AAAA"].Desired, []string{"2606:4700:4700::1111"}) || !slices.Equal(v6[key].Desired, []string{"103.26.8.38"}) {
		t.Fatalf("address families: %+v", v6)
	}
	remote := []dnsRemoteRecord{{ID: "sg", Name: "sg.reiiv.com", Type: "A", Line: "default_view", Status: "ACTIVE", TTL: 300, Values: []string{"103.26.8.38"}}}
	for _, uncertain := range []bool{false, true} {
		for _, r := range dnsBuildPlan(c, hk, remote, map[string]bool{key: true}, uncertain) {
			if r.Name != "sg.reiiv.com" {
				continue
			}
			if uncertain && r.Action != "hold" || !uncertain && (r.Action != "update" || !slices.Equal(r.Desired, want)) {
				t.Fatalf("plan safety: %+v", r)
			}
		}
	}
	fewer := dnsDesired(c, hk[:2])[key]
	if len(fewer.Desired) != 2 {
		t.Fatalf("fewer than three: %+v", fewer)
	}
	for _, r := range dnsBuildPlan(c, recovered, []dnsRemoteRecord{{ID: "sg", Name: "sg.reiiv.com", Type: "A", Line: "default_view", Status: "ACTIVE", TTL: 300, Values: want}}, nil, false) {
		if r.Name == "sg.reiiv.com" && (r.Action != "update" || !slices.Equal(r.Desired, []string{"103.26.8.38"})) {
			t.Fatalf("recover plan: %+v", r)
		}
	}
}
