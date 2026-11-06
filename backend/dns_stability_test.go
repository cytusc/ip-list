package main

import (
	"math"
	"testing"
	"time"
)

func TestDNSP95MeanDropsLatencyPeaks(t *testing.T) {
	now := time.Now()
	samples := make([]dnsSample, 0, 20)
	for i := 0; i < 19; i++ {
		samples = append(samples, dnsSample{At: now, Success: true, Delay: 38})
	}
	samples = append(samples, dnsSample{At: now, Success: true, Delay: 1000})
	got, ok := dnsP95Mean(samples)
	if !ok || got != 38 {
		t.Fatalf("mean=%v ok=%v", got, ok)
	}
}

func TestDNSRegionalLatencyMargins(t *testing.T) {
	candidates := []dnsCandidate{
		{IP: "192.0.2.1", Pool: "node", Country: "HK", State: "healthy", Delay: 38},
		{IP: "192.0.2.2", Pool: "node", Country: "HK", State: "healthy", Delay: 48},
		{IP: "192.0.2.3", Pool: "node", Country: "JP", State: "healthy", Delay: 48},
		{IP: "192.0.2.4", Pool: "node", Country: "US", State: "healthy", Delay: 63},
		{IP: "192.0.2.5", Pool: "node", Country: "DE", State: "healthy", Delay: 63.01},
		{IP: "192.0.2.6", Pool: "node", Country: "FR", State: "healthy", Delay: 63},
		{IP: "192.0.2.7", Pool: "cf", Country: "HK", State: "healthy", Delay: 90},
		{IP: "192.0.2.8", Pool: "cf", Country: "AU", State: "healthy", Delay: 100},
	}
	dnsSelectLatency(candidates)
	for i, want := range []string{"healthy", "healthy", "healthy", "healthy", "pending", "healthy", "healthy", "healthy"} {
		if candidates[i].State != want {
			t.Fatalf("%s: state=%s want=%s", candidates[i].IP, candidates[i].State, want)
		}
	}
	if !dnsAsiaPacific("NZ") || !dnsAsiaPacific("AU") || dnsAsiaPacific("US") {
		t.Fatal("unexpected Asia-Pacific classification")
	}
}

func TestDNSBriefLossAndContinuousFailure(t *testing.T) {
	now := time.Now()
	c := dnsTestConfig()
	c.Window = 10
	sample := func(seconds int, ok bool) dnsSample {
		return dnsSample{At: now.Add(-time.Duration(seconds) * time.Second), Success: ok, Delay: 38}
	}
	cases := []struct {
		name    string
		samples []dnsSample
		want    string
	}{
		{"short outage remains eligible", []dnsSample{sample(100, true), sample(80, false), sample(60, true), sample(40, true), sample(20, false), sample(1, false)}, "healthy"},
		{"less than five minutes failing", []dnsSample{sample(240, true), sample(180, false), sample(120, false), sample(60, false), sample(1, false)}, "healthy"},
		{"five minutes continuously failing", []dnsSample{sample(360, true), sample(301, false), sample(240, false), sample(180, false), sample(120, false), sample(60, false), sample(1, false)}, "failed"},
		{"missing reports break the failure streak", []dnsSample{sample(400, false), sample(1, false)}, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := dnsProbeHealth(tc.samples, now, 90*time.Second, c)
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
	if _, ok := dnsP95Mean([]dnsSample{{Success: true, Delay: math.NaN()}}); ok {
		t.Fatal("accepted invalid delay")
	}
}
