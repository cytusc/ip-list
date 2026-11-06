package main

import (
	"math"
	"slices"
	"testing"
)

func TestWebsiteDNSUsesSharedLatencySelection(t *testing.T) {
	c := dnsTestConfig()
	c.WebsiteEnabled = true
	c.MaxIPs = 10
	candidates := []dnsCandidate{
		{IP: "1.1.1.1", Country: "HK", Pool: "cf", State: "healthy", Delay: 150},
		{IP: "8.8.8.8", Country: "JP", Pool: "cf", State: "healthy", Delay: 48},
		{IP: "9.9.9.9", Country: "SG", Pool: "cf", State: "healthy", Delay: 48},
		{IP: "8.8.4.4", Country: "US", Pool: "cf", State: "healthy", Delay: 58},
		{IP: "1.0.0.1", Country: "US", Pool: "cf", State: "healthy", Delay: 58.1},
		{IP: "208.67.222.222", Country: "FR", Pool: "cf", State: "healthy", Delay: 68},
		{IP: "208.67.220.220", Country: "HK", Pool: "cf", State: "unknown"},
		{IP: "4.2.2.1", Country: "HK", Pool: "cf", State: "healthy", Delay: math.NaN()},
		{IP: "4.2.2.2", Country: "HK", State: "healthy", Delay: 10},
	}
	dnsSelectLatency(candidates)
	got := dnsDesired(c, candidates)["url.reiiv.com/A"].Desired
	want := []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
