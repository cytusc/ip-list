package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func dnsTestConfig() dnsConfig {
	return dnsConfig{Domain: "reiiv.com", Interval: 300, TTL: 300, MaxIPs: 10, Window: 5, MinSuccess: 80}
}

func TestDNSHealth(t *testing.T) {
	now := time.Now()
	sample := func(seconds int, ok bool) dnsSample {
		return dnsSample{At: now.Add(time.Duration(-seconds) * time.Second), Success: ok, Delay: 39}
	}
	for _, tc := range []struct {
		name    string
		samples []dnsSample
		want    string
	}{
		{"stable", []dnsSample{sample(80, true), sample(40, true), sample(1, true)}, "healthy"},
		{"persistent failure", []dnsSample{sample(120, false), sample(60, false), sample(1, false)}, "failed"},
		{"short loss", []dnsSample{sample(90, true), sample(40, true), sample(1, false)}, "unknown"},
		{"poor success ratio", []dnsSample{sample(90, false), sample(40, false), sample(1, true)}, "failed"},
		{"stale", []dnsSample{sample(240, true), sample(180, true)}, "unknown"},
		{"sparse", []dnsSample{sample(40, true), sample(1, true)}, "unknown"},
		{"future ignored", []dnsSample{sample(-30, true), sample(1, true)}, "unknown"},
		{"missing", nil, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := dnsProbeHealth(tc.samples, now, 90*time.Second, dnsTestConfig())
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestDNSCountryAndDesired(t *testing.T) {
	for _, code := range []string{"HK", "MO", "TW", "JP", "US"} {
		if dnsCountry(code) != strings.ToLower(code) {
			t.Fatal(code)
		}
	}
	for _, code := range []string{"ZZ", "EU", "001", "hk.foo", ""} {
		if dnsCountry(code) != "" {
			t.Fatal(code)
		}
	}
	c := dnsTestConfig()
	c.MaxIPs = 1
	candidates := []dnsCandidate{
		{IP: "191.222.220.126", Country: "HK", State: "healthy", Delay: 39},
		{IP: "47.243.169.208", Country: "HK", State: "healthy", Delay: 55},
		{IP: "103.135.251.114", Country: "HK", State: "failed"},
		{IP: "8.8.8.8", Country: "US", State: "unknown"},
		{IP: "2606:4700:4700::1111", Country: "JP", State: "healthy", Delay: 20},
		{IP: "127.0.0.1", Country: "HK", State: "healthy"},
	}
	desired := dnsDesired(c, candidates)
	if len(desired) != 2 || !slices.Equal(desired["hk.reiiv.com/A"].Desired, []string{"191.222.220.126"}) || desired["jp.reiiv.com/AAAA"].Type != "AAAA" {
		t.Fatal(desired)
	}
	c.Domain = "REIIV.COM."
	if err := c.validate(); err != nil || c.Domain != "reiiv.com" {
		t.Fatal(c, err)
	}
	c.Domain = "bad/host"
	if c.validate() == nil {
		t.Fatal("accepted invalid domain")
	}
}

func TestDNSContinuousFailurePolicy(t *testing.T) {
	now := time.Now()
	c := dnsTestConfig()
	c.MinSuccess = 0
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	sample := func(seconds int, ok bool) dnsSample {
		return dnsSample{At: now.Add(-time.Duration(seconds) * time.Second), Success: ok, Delay: 70}
	}
	for _, tc := range []struct {
		name    string
		samples []dnsSample
		want    string
	}{
		{"intermittent loss below 80 percent", []dnsSample{sample(120, true), sample(100, false), sample(80, false), sample(60, true), sample(40, false), sample(20, true), sample(1, true)}, "healthy"},
		{"brief latest timeout", []dnsSample{sample(100, true), sample(80, false), sample(60, true), sample(40, true), sample(20, false), sample(1, false)}, "healthy"},
		{"continuous failure 60 seconds", []dnsSample{sample(100, true), sample(61, false), sample(41, false), sample(21, false), sample(1, false)}, "failed"},
		{"never reached", []dnsSample{sample(81, false), sample(61, false), sample(41, false), sample(21, false), sample(1, false)}, "failed"},
		{"missing reports interrupt failure streak", []dnsSample{sample(200, false), sample(1, false)}, "unknown"},
		{"old success cannot justify recent timeout", []dnsSample{sample(100, true), sample(20, false), sample(1, false)}, "unknown"},
		{"stale samples", []dnsSample{sample(240, true), sample(180, true)}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, delay := dnsProbeHealth(tc.samples, now, 90*time.Second, c)
			if got != tc.want || got == "healthy" && delay != 70 {
				t.Fatalf("state=%s delay=%v, want %s", got, delay, tc.want)
			}
		})
	}
}

func TestDNSPublishesEveryHealthyJapaneseIP(t *testing.T) {
	ips := []string{"45.66.129.24", "45.66.129.48", "45.66.129.225", "45.66.129.247", "45.153.245.217", "152.175.34.232", "152.175.36.150", "191.222.212.167", "191.222.214.71"}
	candidates := []dnsCandidate{{IP: "103.135.251.114", Country: "JP", State: "failed"}}
	for _, ip := range ips {
		candidates = append(candidates, dnsCandidate{IP: ip, Country: "JP", State: "healthy", Delay: 70})
	}
	plan := dnsBuildPlan(dnsTestConfig(), candidates, nil, nil, false)
	if len(plan) != 1 || plan[0].Name != "jp.reiiv.com" || len(plan[0].Desired) != len(ips) {
		t.Fatalf("expected all nine healthy IPs in Japan record: %+v", plan)
	}
	for _, ip := range ips {
		if !slices.Contains(plan[0].Desired, ip) {
			t.Fatalf("healthy IP missing: %s", ip)
		}
	}
}

func TestDNSPlans(t *testing.T) {
	c := dnsTestConfig()
	hk := dnsRemoteRecord{ID: "hk", Name: "hk.reiiv.com", Type: "A", Line: "default_view", Status: "ACTIVE", TTL: 300, Values: []string{"103.135.251.114", "191.222.220.126"}}
	healthy := []dnsCandidate{{IP: "191.222.220.126", Country: "HK", State: "healthy"}, {IP: "8.8.8.8", Country: "US", State: "healthy"}, {IP: "103.135.251.114", Country: "HK", State: "failed"}}
	p := dnsBuildPlan(c, healthy, []dnsRemoteRecord{hk}, nil, false)
	if len(p) != 2 || p[0].Action != "update" || !slices.Equal(p[0].Desired, []string{"191.222.220.126"}) || p[1].Name != "us.reiiv.com" || p[1].Action != "create" {
		t.Fatal(p)
	}
	for _, tc := range []struct {
		name      string
		ips       []dnsCandidate
		records   []dnsRemoteRecord
		managed   map[string]bool
		uncertain bool
		want      string
	}{
		{"monitor outage", healthy, []dnsRemoteRecord{hk}, nil, true, "hold"},
		{"unknown country sample", append(healthy, dnsCandidate{IP: "47.243.107.190", Country: "HK", State: "unknown"}), []dnsRemoteRecord{hk}, nil, false, "update"},
		{"owned empty", nil, []dnsRemoteRecord{hk}, map[string]bool{"hk.reiiv.com/A": true}, false, "delete"},
		{"unowned empty", nil, []dnsRemoteRecord{hk}, nil, false, "hold"},
		{"duplicate line", healthy, []dnsRemoteRecord{hk, hk}, nil, false, "hold"},
		{"cname", healthy, []dnsRemoteRecord{hk, {ID: "cname", Name: "hk.reiiv.com", Type: "CNAME"}}, nil, false, "hold"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := dnsBuildPlan(c, tc.ips, tc.records, tc.managed, tc.uncertain)
			if got[0].Action != tc.want {
				t.Fatal(got)
			}
		})
	}
	preserved := dnsBuildPlan(c, append(healthy, dnsCandidate{IP: "103.135.251.114", Country: "HK", State: "unknown"}), []dnsRemoteRecord{hk}, nil, false)
	if !slices.Contains(preserved[0].Desired, "103.135.251.114") {
		t.Fatal("lost uncertain published IP", preserved)
	}
	other := hk
	other.Line = "Dianxin"
	other.Name = "www.reiiv.com"
	if p := dnsBuildPlan(c, nil, []dnsRemoteRecord{other}, nil, false); len(p) != 0 {
		t.Fatal(p)
	}
	changed := hk
	changed.Values = []string{"1.1.1.1"}
	if dnsSameRemote([]dnsRemoteRecord{hk}, []dnsRemoteRecord{changed}, hk.Name, hk.Type) {
		t.Fatal("missed manual edit")
	}
	if !dnsSameRemote([]dnsRemoteRecord{hk}, []dnsRemoteRecord{hk, other}, hk.Name, hk.Type) {
		t.Fatal("unrelated change")
	}
}

func TestDNSProbeFailureEvidencePreservesRecords(t *testing.T) {
	remote := dnsRemoteRecord{ID: "hk", Name: "hk.reiiv.com", Type: "A", Line: "default_view", Status: "ACTIVE", TTL: 300, Values: []string{"103.135.251.114"}}
	for _, tc := range []struct {
		name     string
		state    string
		otherOK  bool
		preserve bool
	}{
		{"probe network outage", "failed", false, true},
		{"missing reports", "unknown", true, true},
		{"confirmed target failure", "failed", true, false},
		{"healthy target", "healthy", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := dnsCandidate{IP: remote.Values[0], Country: "HK", State: dnsCorroborateFailure(tc.state, tc.otherOK)}
			plan := dnsBuildPlan(dnsTestConfig(), []dnsCandidate{candidate}, []dnsRemoteRecord{remote}, map[string]bool{"hk.reiiv.com/A": true}, false)
			if len(plan) != 1 || slices.Contains(plan[0].Desired, remote.Values[0]) != tc.preserve {
				t.Fatalf("unexpected plan: %+v", plan)
			}
		})
	}
}

type dnsFakeProvider struct {
	records []dnsRemoteRecord
	writes  []dnsPlanRecord
	fail    bool
}

func (f *dnsFakeProvider) Zone(context.Context, string) (string, error) { return "dns-test-zone", nil }
func (f *dnsFakeProvider) Records(context.Context, string) ([]dnsRemoteRecord, error) {
	return f.records, nil
}
func (f *dnsFakeProvider) Apply(_ context.Context, _ string, r dnsPlanRecord, _ int32) error {
	if f.fail {
		return errors.New("test cloud failure")
	}
	f.writes = append(f.writes, r)
	return nil
}

func TestDNSIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires TEST_DATABASE_URL")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !strings.HasSuffix(db.Config().ConnConfig.Database, "_test") {
		t.Fatal("requires test database")
	}
	if _, err = db.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	protocol := "dns/test"
	_, err = db.Exec(ctx, `INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at) VALUES
 ('dns/test','191.222.220.126',true,39,now()),('dns/test','103.135.251.114',true,39,now()),('dns/test','8.8.8.8',true,39,now());
 INSERT INTO dns_ip_locations(ip,country) VALUES('191.222.220.126','HK'),('8.8.8.8','US') ON CONFLICT(ip) DO UPDATE SET country=excluded.country,checked_at=now()`)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, "DELETE FROM node_ip_pool WHERE protocol=$1", protocol)
	defer db.Exec(ctx, "DELETE FROM dns_managed_records WHERE zone_id='dns-test-zone'")
	attachPoolConfig(t, db, protocol)
	var probe string
	err = db.QueryRow(ctx, `INSERT INTO latency_probes(name,token_hash,last_seen,heartbeat_at) VALUES($1,$2,now(),now()) RETURNING id::text`, "dns-test-"+time.Now().Format("150405.000000"), []byte(time.Now().String())).Scan(&probe)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, "DELETE FROM latency_probes WHERE id=$1", probe)
	_, err = db.Exec(ctx, `INSERT INTO latency_probe_results(probe_id,ip,success,delay_ms,checked_at)
 SELECT $1::uuid,ip::inet,ip<>'103.135.251.114',39,now()-make_interval(secs=>age)
 FROM unnest(ARRAY['191.222.220.126','103.135.251.114','8.8.8.8']) ip CROSS JOIN unnest(ARRAY[121,81,41,1]) age`, probe)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{db: db}
	c := dnsTestConfig()
	c.Protocol = protocol
	c.ProbeID = probe
	fake := &dnsFakeProvider{records: []dnsRemoteRecord{{ID: "hk", Name: "hk.reiiv.com", Type: "A", Line: "default_view", Status: "ACTIVE", TTL: 300, Values: []string{"191.222.220.126", "103.135.251.114"}}}}
	report := dnsReport{Mode: "preview"}
	if err = a.executeDNS(ctx, c, &report, fake); err != nil {
		t.Fatal(err)
	}
	if len(fake.writes) != 0 || len(report.Records) != 2 || report.Records[0].Action != "update" || len(report.Records[0].Desired) != 1 {
		t.Fatal(report)
	}
	for _, ip := range report.Candidates {
		if ip.IP == "103.135.251.114" && ip.State != "failed" {
			t.Fatal(ip)
		}
	}
	report.Mode = "sync"
	if err = a.executeDNS(ctx, c, &report, fake); err != nil {
		t.Fatal(err)
	}
	if len(fake.writes) != 2 || !report.Records[0].Applied {
		t.Fatal(report)
	}
	// A live heartbeat with a broken probe network must not remove IPs.
	if _, err = db.Exec(ctx, "UPDATE latency_probe_results SET success=false WHERE probe_id=$1", probe); err != nil {
		t.Fatal(err)
	}
	fake.writes = nil
	if err = a.executeDNS(ctx, c, &report, fake); err != nil || len(fake.writes) != 0 || !slices.Contains(report.Records[0].Desired, "103.135.251.114") {
		t.Fatalf("network outage removed record: %+v, %v", report, err)
	}
	if _, err = db.Exec(ctx, "UPDATE latency_probe_results SET success=true WHERE probe_id=$1 AND ip='191.222.220.126'", probe); err != nil {
		t.Fatal(err)
	}
	fake.fail = true
	if err = a.executeDNS(ctx, c, &report, fake); err == nil || report.Records[0].Applied {
		t.Fatal("cloud error not reported", report)
	}
	_, err = db.Exec(ctx, "UPDATE latency_probes SET last_seen=now()-interval '1 hour' WHERE id=$1", probe)
	if err != nil {
		t.Fatal(err)
	}
	fake.fail = false
	fake.writes = nil
	if err = a.executeDNS(ctx, c, &report, fake); err != nil {
		t.Fatal(err)
	}
	if len(fake.writes) != 0 || report.Records[0].Action != "hold" {
		t.Fatal(report)
	}
	// Auth is covered by the protected router; exercise settings/queue SQL here.
	saved, err := a.loadDNSConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		raw, _ := json.Marshal(saved)
		db.Exec(ctx, "UPDATE dns_settings SET config=$1,requested='' WHERE id", string(raw))
	}()
	valid := dnsTestConfig()
	body, _ := json.Marshal(valid)
	saveRequest := func() *http.Request {
		r := httptest.NewRequest("PUT", "/dns", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	w := httptest.NewRecorder()
	a.saveDNS(w, saveRequest())
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	t.Setenv("HUAWEICLOUD_SDK_AK", "test-ak")
	t.Setenv("HUAWEICLOUD_SDK_SK", "test-sk")
	req := httptest.NewRequest("POST", "/dns/preview", nil)
	req.SetPathValue("mode", "preview")
	w = httptest.NewRecorder()
	a.requestDNS(w, req)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.requestDNS(w, req)
	if w.Code != 409 {
		t.Fatal("duplicate queue", w.Code)
	}
	lock, err := a.dnsLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	a.saveDNS(w, saveRequest())
	dnsUnlock(lock)
	if w.Code != 409 {
		t.Fatal("running edit", w.Code)
	}
	t.Setenv("HUAWEICLOUD_SDK_AK", "")
	a.runDNSOnce(ctx)
	var running bool
	var requested, runError string
	err = db.QueryRow(ctx, "SELECT running,requested,report->>'error' FROM dns_settings WHERE id").Scan(&running, &requested, &runError)
	if err != nil || running || requested != "" || runError == "" {
		t.Fatal("worker did not persist error", running, requested, runError, err)
	}
	w = httptest.NewRecorder()
	a.getDNS(w, httptest.NewRequest("GET", "/dns", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}

}
