package main

import (
	"context"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDNSStabilityProbeAggregation(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	_, err := a.db.Exec(ctx, `
 INSERT INTO cf_ip_pool(ip,available,delay_ms,http_status,checked_at) VALUES('103.26.8.38',true,65,200,now());
 INSERT INTO latency_probes(name,token_hash,last_seen,heartbeat_at) VALUES('stable','stable',now(),now()),('shaky','shaky',now(),now());
 INSERT INTO latency_probe_results(probe_id,ip,success,delay_ms,checked_at)
 SELECT p.id,'103.26.8.38',true,CASE WHEN p.name='shaky' AND age%60=0 THEN 600 ELSE 65 END,now()-make_interval(secs=>age)
 FROM latency_probes p CROSS JOIN generate_series(1,601,20) age;
 INSERT INTO latency_probe_results(probe_id,ip,success,delay_ms,checked_at)
 SELECT id,'1.1.1.1',true,20,now() FROM latency_probes;`)
	if err != nil {
		t.Fatal(err)
	}
	c := dnsTestConfig()
	c.MinSuccess = 0
	check := func(want string, ready bool) {
		t.Helper()
		items, uncertain, err := a.dnsPoolCandidates(ctx, c, true)
		if err != nil || uncertain || len(items) != 1 || items[0].State != want || items[0].StabilityReady != ready {
			t.Fatalf("items=%+v uncertain=%v err=%v", items, uncertain, err)
		}
		if want == "failed" && !strings.Contains(items[0].Reason, "波动") {
			t.Fatal(items)
		}
	}
	check("healthy", true)
	_, err = a.db.Exec(ctx, `WITH ordered AS (SELECT id,row_number() OVER(PARTITION BY probe_id ORDER BY checked_at) n FROM latency_probe_results WHERE ip='103.26.8.38') UPDATE latency_probe_results r SET delay_ms=CASE WHEN o.n%3=0 THEN 600 ELSE 65 END FROM ordered o WHERE r.id=o.id`)
	if err != nil {
		t.Fatal(err)
	}
	check("failed", false)
	_, err = a.db.Exec(ctx, `DELETE FROM latency_probe_results WHERE ip='1.1.1.1'`)
	if err != nil {
		t.Fatal(err)
	}
	check("unknown", false)
}

func TestDNSProbeStability(t *testing.T) {
	now := time.Now()
	base := []dnsSample{}
	for age := 600; age >= 0; age -= 20 {
		base = append(base, dnsSample{At: now.Add(-time.Duration(age) * time.Second), Success: true, Delay: 65})
	}
	for _, tc := range []struct {
		name       string
		mutate     func([]dnsSample) []dnsSample
		bad, ready bool
	}{
		{"stable", func(s []dnsSample) []dnsSample { return s }, false, true},
		{"consistently slow is not jitter", func(s []dnsSample) []dnsSample {
			for i := range s {
				s[i].Delay = 400
			}
			return s
		}, false, true},
		{"large repeated spikes", func(s []dnsSample) []dnsSample {
			for i := range s {
				if i%3 == 0 {
					s[i].Delay = 600
				}
			}
			return s
		}, true, false},
		{"repeated loss", func(s []dnsSample) []dnsSample {
			for i := range s {
				if i%3 == 0 {
					s[i].Success = false
				}
			}
			return s
		}, true, false},
		{"single outlier tolerated", func(s []dnsSample) []dnsSample { s[25].Delay = 900; return s }, false, true},
		{"not enough recovery time", func(s []dnsSample) []dnsSample { return s[16:] }, false, false},
		{"stale", func(s []dnsSample) []dnsSample { return s[:20] }, false, false},
		{"report gap", func(s []dnsSample) []dnsSample { return append(s[:10], s[17:]...) }, false, false},
		{"invalid delays count as loss", func(s []dnsSample) []dnsSample {
			for i := range s {
				if i%3 == 0 {
					s[i].Delay = math.NaN()
				}
			}
			return s
		}, true, false},
		{"recovery requires lower jitter", func(s []dnsSample) []dnsSample {
			for i := range s {
				if i%3 == 0 {
					s[i].Delay = 150
				}
			}
			return s
		}, false, false},
		{"recently calmed still waiting", func(s []dnsSample) []dnsSample {
			for i := 0; i < 15; i++ {
				s[i].Delay = 600
			}
			return s
		}, false, false},
		{"unordered", func(s []dnsSample) []dnsSample { slices.Reverse(s); return s }, false, true},
		{"future ignored", func(s []dnsSample) []dnsSample {
			return append(s, dnsSample{At: now.Add(time.Minute), Success: true, Delay: 9000})
		}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason, ready := dnsProbeStability(tc.mutate(slices.Clone(base)), now, 90*time.Second)
			if (reason != "") != tc.bad || ready != tc.ready {
				t.Fatalf("reason=%q ready=%v", reason, ready)
			}
		})
	}
}

func TestDNSStabilityRemovalRecoveryAndFallback(t *testing.T) {
	c := dnsTestConfig()
	remote := []dnsRemoteRecord{{Name: "sg.reiiv.com", Type: "A", Line: "default_view", Values: []string{"103.26.8.38"}}}
	candidates := []dnsCandidate{
		{IP: "103.26.8.38", Country: "SG", State: "failed", Reason: "近期延迟波动过大"},
		{IP: "47.75.118.189", Country: "HK", State: "healthy", Delay: 34, StabilityReady: true},
	}
	dnsStabilizeCandidates(c, candidates, remote)
	plan := dnsBuildPlan(c, candidates, remote, nil, false)
	if !slices.Equal(plan[len(plan)-1].Desired, []string{"47.75.118.189"}) {
		t.Fatal(plan)
	}
	for _, ready := range []bool{false, true} {
		candidates[0].State, candidates[0].StabilityReady = "healthy", ready
		dnsStabilizeCandidates(c, candidates, nil)
		want := "pending"
		if ready {
			want = "healthy"
		}
		if candidates[0].State != want {
			t.Fatal(candidates)
		}
	}
	candidates[0].State, candidates[0].StabilityReady = "healthy", false
	dnsStabilizeCandidates(c, candidates, remote)
	if candidates[0].State != "healthy" {
		t.Fatal("published IP must use removal threshold")
	}
	// An unrelated/custom DNS line must not bypass stable admission.
	remote[0].Line = "Dianxin"
	dnsStabilizeCandidates(c, candidates, remote)
	if candidates[0].State != "pending" {
		t.Fatal(candidates)
	}
}
