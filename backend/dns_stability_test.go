package main

import (
	"context"
	"math"
	"slices"
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
		{Country: "HK", State: "healthy", Delay: 38},
		{Country: "HK", State: "healthy", Delay: 48},
		{Country: "HK", State: "healthy", Delay: 48.01},
		{Country: "US", State: "healthy", Delay: 140},
		{Country: "us", State: "healthy", Delay: 160},
		{Country: "US", State: "healthy", Delay: 160.01},
		{Country: "JP", State: "healthy", Delay: 70},
		{Country: "JP", State: "healthy", Delay: 80},
		{Country: "JP", State: "healthy", Delay: 80.01},
		{Country: "HK", Pool: "cf", State: "healthy", Delay: 90},
		{Country: "HK", Pool: "cf", State: "healthy", Delay: 100},
		{Country: "HK", Pool: "cf", State: "healthy", Delay: 100.01},
		{Country: "HK", State: "unknown", Delay: 1},
		{Country: "US", State: "failed", Delay: 1},
	}
	dnsSelectLatency(candidates)
	want := []string{"healthy", "healthy", "pending", "healthy", "healthy", "pending", "healthy", "healthy", "pending", "healthy", "healthy", "pending", "unknown", "failed"}
	for i, state := range want {
		if candidates[i].State != state {
			t.Fatalf("candidate %d: got %+v want %s", i, candidates[i], state)
		}
	}
	if !dnsAsiaPacific("NZ") || !dnsAsiaPacific("AU") || !dnsAsiaPacific("JP") || dnsAsiaPacific("US") {
		t.Fatal("unexpected Asia-Pacific classification")
	}
}

func TestDNSRegionalLatencyPlan(t *testing.T) {
	candidates := []dnsCandidate{
		{IP: "1.1.1.1", Country: "HK", State: "healthy", Delay: 38},
		{IP: "1.0.0.1", Country: "HK", State: "healthy", Delay: 48},
		{IP: "8.8.8.8", Country: "US", State: "healthy", Delay: 140},
		{IP: "8.8.4.4", Country: "US", State: "healthy", Delay: 160},
		{IP: "9.9.9.9", Country: "US", State: "healthy", Delay: 160.01},
	}
	dnsSelectLatency(candidates)
	desired := dnsDesired(dnsTestConfig(), candidates)
	if !slices.Equal(desired["hk.reiiv.com/A"].Desired, []string{"1.0.0.1", "1.1.1.1"}) || !slices.Equal(desired["us.reiiv.com/A"].Desired, []string{"8.8.4.4", "8.8.8.8"}) {
		t.Fatal(desired)
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
		{"short outage falls below success threshold", []dnsSample{sample(100, true), sample(80, false), sample(60, true), sample(40, true), sample(20, false), sample(1, false)}, "pending"},
		{"less than five minutes failing", []dnsSample{sample(240, true), sample(180, false), sample(120, false), sample(60, false), sample(1, false)}, "pending"},
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
	for _, window := range []int{2, 5} {
		c.Window = window
		got, _ := dnsProbeHealth(cases[2].samples, now, 90*time.Second, c)
		if got != "failed" {
			t.Fatalf("window %d minutes: got %s want failed", window, got)
		}
	}
	if _, ok := dnsP95Mean([]dnsSample{{Success: true, Delay: math.NaN()}}); ok {
		t.Fatal("accepted invalid delay")
	}
}

func TestDNSSelectedProbeOutage(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	var selected string
	err := a.db.QueryRow(ctx, `INSERT INTO latency_probes(name,token_hash,last_seen,heartbeat_at)
 VALUES('telecom','telecom',now(),now()) RETURNING id::text`).Scan(&selected)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.db.Exec(ctx, `
 INSERT INTO latency_probes(name,token_hash,last_seen,heartbeat_at) VALUES('other','other',now(),now());
 INSERT INTO cf_ip_pool(ip,available,delay_ms,http_status,checked_at) VALUES('103.26.8.38',true,65,200,now());
 INSERT INTO latency_probe_results(probe_id,ip,success,delay_ms,checked_at)
 SELECT p.id,ip::inet,p.name='other' OR ip='1.1.1.1',65,now()-make_interval(secs=>age)
 FROM latency_probes p CROSS JOIN unnest(ARRAY['103.26.8.38','1.1.1.1']) ip CROSS JOIN generate_series(1,601,20) age`)
	if err != nil {
		t.Fatal(err)
	}
	c := dnsTestConfig()
	c.ProbeID = selected
	check := func(want string, outage bool) {
		t.Helper()
		items, uncertain, err := a.dnsPoolCandidates(ctx, c, true)
		if err != nil || uncertain != outage || len(items) != 1 || items[0].State != want {
			t.Fatalf("items=%+v uncertain=%v err=%v", items, uncertain, err)
		}
	}
	// Other probes' successes must not override the selected probe's failures.
	check("failed", false)
	for _, change := range []string{
		"UPDATE latency_probes SET last_seen=now()-interval '1 hour' WHERE id=$1",
		"UPDATE latency_probes SET last_seen=now(),enabled=false WHERE id=$1",
		`WITH p AS (UPDATE latency_probes SET enabled=true WHERE id=$1 RETURNING id)
 UPDATE latency_probe_results SET checked_at=checked_at-interval '1 hour' WHERE probe_id IN (SELECT id FROM p)`,
	} {
		if _, err = a.db.Exec(ctx, change, selected); err != nil {
			t.Fatal(err)
		}
		check("unknown", true)
	}
	if _, err = a.db.Exec(ctx, `INSERT INTO latency_probe_results(probe_id,ip,success,delay_ms,checked_at)
 SELECT $1::uuid,'103.26.8.38',true,65,now()-make_interval(secs=>age) FROM generate_series(1,601,20) age`, selected); err != nil {
		t.Fatal(err)
	}
	check("healthy", false)
}

func TestDNSLatestControlEvidence(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	_, err := a.db.Exec(ctx, `
 INSERT INTO cf_ip_pool(ip,available,delay_ms,http_status,checked_at) VALUES('103.26.8.38',true,65,200,now());
 INSERT INTO latency_probes(name,token_hash,last_seen,heartbeat_at) VALUES('control','control',now(),now());
 INSERT INTO latency_probe_results(probe_id,ip,success,delay_ms,checked_at)
 SELECT id,'103.26.8.38',false,0,now()-make_interval(secs=>age)
 FROM latency_probes CROSS JOIN generate_series(1,601,20) age;
 INSERT INTO latency_probe_results(probe_id,ip,success,delay_ms,checked_at)
 SELECT id,'1.1.1.1',true,20,now()-interval '10 seconds' FROM latency_probes;
 INSERT INTO latency_probe_results(probe_id,ip,success,delay_ms,checked_at)
 SELECT id,'1.1.1.1',false,0,now()-interval '10 seconds' FROM latency_probes;
 INSERT INTO latency_probe_results(probe_id,ip,mode,tcp_port,success,delay_ms,checked_at)
 SELECT id,'8.8.8.8','tcp',443,true,20,now() FROM latency_probes;`)
	if err != nil {
		t.Fatal(err)
	}
	check := func(want string) {
		t.Helper()
		items, uncertain, err := a.dnsPoolCandidates(ctx, dnsTestConfig(), true)
		if err != nil || uncertain || len(items) != 1 || items[0].State != want {
			t.Fatalf("items=%+v uncertain=%v err=%v", items, uncertain, err)
		}
	}
	check("unknown")
	// A fresh success outside the publication pool is still valid control evidence.
	_, err = a.db.Exec(ctx, `INSERT INTO latency_probe_results(probe_id,ip,success,delay_ms,checked_at) SELECT id,'9.9.9.9',true,20,now() FROM latency_probes`)
	if err != nil {
		t.Fatal(err)
	}
	check("failed")
	_, err = a.db.Exec(ctx, `UPDATE latency_probe_results SET checked_at=now()-interval '1 hour' WHERE ip='9.9.9.9'`)
	if err != nil {
		t.Fatal(err)
	}
	check("unknown")
}
