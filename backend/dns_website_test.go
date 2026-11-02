package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebsiteDNSSelection(t *testing.T) {
	c := dnsTestConfig()
	c.WebsiteEnabled = true
	candidates := []dnsCandidate{
		{IP: "1.1.1.1", Country: "HK", State: "healthy", Delay: 150, Pool: "cf"},
		{IP: "8.8.8.8", Country: "JP", State: "healthy", Delay: 80, Pool: "cf"},
		{IP: "9.9.9.9", Country: "SG", State: "healthy", Delay: 79, Pool: "cf"},
		{IP: "8.8.4.4", Country: "JP", State: "healthy", Delay: 80.01, Pool: "cf"},
		{IP: "1.0.0.1", Country: "SG", State: "healthy", Delay: 81, Pool: "cf"},
		{IP: "208.67.222.222", Country: "US", State: "healthy", Delay: 10, Pool: "cf"},
		{IP: "208.67.220.220", Country: "HK", State: "unknown", Pool: "cf"},
		{IP: "4.2.2.1", Country: "HK", State: "healthy", Delay: math.NaN(), Pool: "cf"},
		{IP: "4.2.2.2", Country: "HK", State: "healthy", Delay: 10},
	}
	desired := dnsDesired(c, candidates)
	url := desired["url.reiiv.com/A"]
	if strings.Join(url.Desired, ",") != "1.1.1.1,8.8.8.8,9.9.9.9" {
		t.Fatal(desired)
	}
	if len(desired) != 2 || len(desired["hk.reiiv.com/A"].Desired) != 1 {
		t.Fatal(desired)
	}
	old := dnsRemoteRecord{Name: "url.reiiv.com", Type: "A", Line: "default_view", Status: "ACTIVE", TTL: 300, Values: []string{"8.8.4.4"}}
	plan := dnsBuildPlan(c, candidates, []dnsRemoteRecord{old}, map[string]bool{"url.reiiv.com/A": true}, false)
	for _, r := range plan {
		if r.Name == old.Name && (r.Action != "update" || len(r.Desired) != 3) {
			t.Fatal(plan)
		}
	}
	c.WebsiteEnabled = false
	if _, ok := dnsDesired(c, candidates)["url.reiiv.com/A"]; ok {
		t.Fatal("website enabled unexpectedly")
	}
	if plan := dnsBuildPlan(c, nil, []dnsRemoteRecord{old}, nil, false); len(plan) != 0 {
		t.Fatal(plan)
	}
}

func TestWebsiteProbePoolAndAverage(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	token, hash, err := newProbeToken()
	if err != nil {
		t.Fatal(err)
	}
	var probe string
	err = a.db.QueryRow(ctx, `INSERT INTO latency_probes(name,token_hash,last_seen,heartbeat_at) VALUES('website', $1,now(),now()) RETURNING id::text`, hash).Scan(&probe)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.db.Exec(ctx, `INSERT INTO cf_ip_pool(ip,available,delay_ms,http_status,checked_at) VALUES('8.8.8.8',true,0,200,now()),('1.1.1.1',true,0,200,now()),('9.9.9.9',false,0,500,now()); INSERT INTO ip_blacklist(ip) VALUES('1.1.1.1');`)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/probe-agent/targets", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	a.probeAgentTargets(response, req)
	var targets struct {
		IPs []string `json:"ips"`
	}
	json.Unmarshal(response.Body.Bytes(), &targets)
	if response.Code != 200 || len(targets.IPs) != 1 || targets.IPs[0] != "8.8.8.8" {
		t.Fatal(response.Body.String())
	}
	for _, age := range []int{90, 1} {
		body := `{"results":[{"ip":"8.8.8.8","success":true,"delay_ms":60,"checked_at":"` + time.Now().Add(-time.Duration(age)*time.Second).UTC().Format(time.RFC3339Nano) + `"}]}`
		req = httptest.NewRequest(http.MethodPost, "/probe-agent/results", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		response = httptest.NewRecorder()
		a.reportProbeResults(response, req)
		if response.Code != 200 {
			t.Fatal(response.Body.String())
		}
	}
	// A second healthy probe with 120ms must not let the fastest probe's 60ms
	// bypass the 80ms threshold: overall mean is 90ms.
	_, err = a.db.Exec(ctx, `WITH p AS (INSERT INTO latency_probes(name,token_hash,last_seen,heartbeat_at) VALUES('website2','second',now(),now()) RETURNING id) INSERT INTO latency_probe_results(probe_id,ip,success,delay_ms,checked_at) SELECT p.id,'8.8.8.8',true,120,now()-make_interval(secs=>age) FROM p CROSS JOIN unnest(ARRAY[90,1]) age`)
	if err != nil {
		t.Fatal(err)
	}
	candidates, uncertain, err := a.dnsPoolCandidates(ctx, dnsTestConfig(), true)
	if err != nil || uncertain || len(candidates) != 1 || candidates[0].Delay != 90 || candidates[0].State != "healthy" || candidates[0].Pool != "cf" {
		t.Fatalf("%+v uncertain=%v err=%v", candidates, uncertain, err)
	}
	candidates[0].Country = "JP"
	c := dnsTestConfig()
	c.WebsiteEnabled = true
	if len(dnsDesired(c, candidates)) != 0 {
		t.Fatal("published JP with 90ms average")
	}
}

func TestWebsiteDNSNotifications(t *testing.T) {
	c := dnsTestConfig()
	c.WebsiteEnabled = true
	report := dnsReport{Mode: "sync", Domain: c.Domain, Config: c,
		Records:    []dnsPlanRecord{{Name: "url.reiiv.com", Type: "A", Action: "create", Applied: true, Desired: []string{"8.8.8.8"}}},
		Candidates: []dnsCandidate{{IP: "8.8.8.8", State: "healthy", Delay: 10}, {IP: "8.8.8.8", State: "healthy", Delay: 75, Pool: "cf", Provider: "网站运营商"}}}
	messages, err := dnsFeishuMessages(report)
	if err != nil || len(messages) != 1 {
		t.Fatalf("%v %v", messages, err)
	}
	raw, _ := json.Marshal(messages)
	if !strings.Contains(string(raw), "75.0 ms") || !strings.Contains(string(raw), "网站运营商") || !strings.Contains(string(raw), "url.reiiv.com") {
		t.Fatal(string(raw))
	}
}

func TestWebsiteDNSStability(t *testing.T) {
	c := dnsTestConfig()
	c.WebsiteEnabled = true
	now := time.Now().UTC()
	item := dnsCandidate{IP: "8.8.8.8", Country: "JP", Pool: "cf", State: "healthy", Delay: 65}
	history := func() []dnsReport {
		result := []dnsReport{}
		for _, minutes := range []int{5, 10, 15} {
			old := item
			old.State = "pending"
			result = append(result, dnsReport{Mode: "sync", Config: c, StartedAt: now.Add(-time.Duration(minutes) * time.Minute), Candidates: []dnsCandidate{old}})
		}
		return result
	}
	for _, tc := range []struct {
		name string
		edit func(*dnsCandidate, []dnsReport) []dnsReport
		want bool
	}{
		{"sustained recovery", func(_ *dnsCandidate, h []dnsReport) []dnsReport { return h }, true},
		{"too little history", func(_ *dnsCandidate, h []dnsReport) []dnsReport { return h[:2] }, false},
		{"admission boundary", func(i *dnsCandidate, h []dnsReport) []dnsReport { i.Delay = 70; return h }, true},
		{"near removal threshold", func(i *dnsCandidate, h []dnsReport) []dnsReport { i.Delay = 75; return h }, false},
		{"spike during stable period", func(_ *dnsCandidate, h []dnsReport) []dnsReport { h[1].Candidates[0].Delay = 81; return h }, false},
		{"missing probe data", func(_ *dnsCandidate, h []dnsReport) []dnsReport { h[1].Candidates[0].State = "unknown"; return h }, false},
		{"sync gap", func(_ *dnsCandidate, h []dnsReport) []dnsReport { return h[2:] }, false},
		{"monitor outage", func(_ *dnsCandidate, h []dnsReport) []dnsReport { h[1].Warning = "monitor unavailable"; return h }, false},
		{"policy changed", func(_ *dnsCandidate, h []dnsReport) []dnsReport { h[1].Config.Window = 20; return h }, false},
		{"recent removal", func(_ *dnsCandidate, h []dnsReport) []dnsReport {
			return append(h, dnsReport{StartedAt: now.Add(-20 * time.Minute), Records: []dnsPlanRecord{{Name: "url.reiiv.com", Applied: true, Action: "update", Current: []string{item.IP}}}})
		}, false},
		{"failed cloud write", func(_ *dnsCandidate, h []dnsReport) []dnsReport {
			return append(h, dnsReport{StartedAt: now.Add(-20 * time.Minute), Records: []dnsPlanRecord{{Name: "url.reiiv.com", Applied: false, Action: "update", Current: []string{item.IP}}}})
		}, true},
		{"cooldown expired", func(_ *dnsCandidate, h []dnsReport) []dnsReport {
			return append(h, dnsReport{StartedAt: now.Add(-30 * time.Minute), Records: []dnsPlanRecord{{Name: "url.reiiv.com", Applied: true, Action: "update", Current: []string{item.IP}}}})
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := item
			h := tc.edit(&candidate, history())
			if got := dnsWebsiteStable(c, candidate, h, now); got != tc.want {
				t.Fatalf("stable=%v want=%v", got, tc.want)
			}
		})
	}
	// Existing healthy entries are retained up to 80ms, but an 81ms entry is
	// removed immediately even if the same address is still healthy in grpc.
	current := []dnsRemoteRecord{{Name: "url.reiiv.com", Type: "A", Status: "ACTIVE", Line: "default_view", TTL: 300, Values: []string{item.IP}}}
	for _, delay := range []float64{75, 81} {
		candidate := item
		candidate.Delay = delay
		candidates := []dnsCandidate{candidate, {IP: "1.1.1.1", Country: "HK", State: "healthy", Delay: 30}}
		dnsStabilizeWebsite(c, candidates, current, nil, now)
		plan := dnsBuildPlan(c, candidates, current, map[string]bool{"url.reiiv.com/A": true}, false)
		for _, r := range plan {
			if r.Name == "url.reiiv.com" {
				want := "unchanged"
				if delay > 80 {
					want = "delete"
				}
				if r.Action != want {
					t.Fatal(r)
				}
			}
		}
	}
	candidates := []dnsCandidate{item, {IP: "1.1.1.1", Country: "HK", State: "healthy", Delay: 30}}
	dnsStabilizeWebsite(c, candidates, nil, nil, now)
	if candidates[0].State != "pending" || candidates[1].State != "healthy" {
		t.Fatal(candidates)
	}
}

func TestWebsiteDNSHistory(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	c := dnsTestConfig()
	c.WebsiteEnabled = true
	now := time.Now().UTC()
	item := dnsCandidate{IP: "8.8.8.8", Country: "JP", Pool: "cf", State: "pending", Delay: 65}
	for _, age := range []int{15, 10, 5} {
		at := now.Add(-time.Duration(age) * time.Minute)
		report := dnsReport{Mode: "sync", Domain: c.Domain, Config: c, StartedAt: at, Candidates: []dnsCandidate{item, {IP: "1.1.1.1", State: "healthy"}}, Records: []dnsPlanRecord{{Name: "hk.reiiv.com"}}}
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = a.db.Exec(ctx, `INSERT INTO dns_sync_runs(started_at,report) VALUES($1,$2)`, at, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	history, err := a.dnsWebsiteHistory(ctx, c)
	if err != nil || len(history) != 3 {
		t.Fatalf("%+v %v", history, err)
	}
	if len(history[0].Candidates) != 1 || len(history[0].Records) != 0 || !dnsWebsiteStable(c, item, history, now) {
		t.Fatal(history)
	}
}
