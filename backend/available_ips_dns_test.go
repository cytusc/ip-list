package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestAvailableIPsIndependentOfDNSSync(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := a.db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO nodes(name,address,share_uri) VALUES
 ('grpc','sample.example','vless://11111111-1111-4111-8111-111111111111@sample.example:443?type=grpc&security=tls');
 INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at,config_hash,certificate)
 SELECT 'grpc',('8.8.4.'||i)::inet,true,20,now(),md5(share_uri),'{"status":"passed"}' FROM nodes CROSS JOIN generate_series(1,30) i;
 INSERT INTO dns_ip_locations(ip,country,policy_version)
 SELECT ('8.8.4.'||i)::inet,CASE WHEN i<=22 THEN 'JP' ELSE 'US' END,3 FROM generate_series(1,30) i;
 UPDATE dns_settings SET config=jsonb_set(config,'{enabled}','true');`)
	verifyPoolFixture(t, a.db)
	checkPage := func(query, total string, count int) {
		t.Helper()
		w := pageRequest(t, a.availableIPs, "/available-ips?"+query, 200)
		var items []availableIP
		if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		if len(items) != count || w.Header().Get("X-Total-Count") != total {
			t.Fatalf("%s: total=%s items=%d", query, w.Header().Get("X-Total-Count"), len(items))
		}
		if total == "28" {
			raw, err := base64.StdEncoding.DecodeString(w.Header().Get("X-List-Meta"))
			if err != nil {
				t.Fatal(err)
			}
			var meta struct {
				AllTotal  int      `json:"allTotal"`
				Countries []string `json:"countries"`
			}
			if err := json.Unmarshal(raw, &meta); err != nil || meta.AllTotal != 28 || len(meta.Countries) != 2 {
				t.Fatalf("unfiltered facets: %s %v", raw, err)
			}
		}
	}
	checkPage("", "30", 20) // No successful sync yet.
	report := dnsReport{Mode: "sync", Domain: "reiiv.com"}
	r := dnsPlanRecord{Name: "jp.reiiv.com", Type: "A", Action: "update", Applied: true}
	for i := 1; i <= 30; i++ {
		ip := fmt.Sprintf("8.8.4.%d", i)
		report.Candidates = append(report.Candidates, dnsCandidate{IP: ip, State: "healthy", Country: "JP"})
		if i <= 28 {
			r.Desired = append(r.Desired, ip)
		}
	}
	report.Candidates[22].State = "pending" // Above the regional latency threshold.
	report.Candidates[23].State = "unknown" // An old DNS address retained during uncertainty.
	report.Candidates[24].Pool = "cf"       // Website success cannot qualify a node.
	report.Candidates[27].State = "failed"
	report.Records = []dnsPlanRecord{
		r,
		{Name: "us.reiiv.com", Type: "A", Action: "hold", Desired: []string{"8.8.4.29"}},
		{Name: "url.reiiv.com", Type: "A", Action: "update", Applied: true, Desired: []string{"8.8.4.30"}},
	}
	exec(`UPDATE node_ip_health SET status='failed' WHERE ip='8.8.4.26';
 INSERT INTO ip_blacklist(ip) VALUES('8.8.4.27')`)
	saveReport := func(report dnsReport, at time.Time) {
		t.Helper()
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		exec("INSERT INTO dns_sync_runs(started_at,report) VALUES($1,$2)", at, string(raw))
	}
	now := time.Now()
	saveReport(report, now)
	// DNS snapshots cannot change the upstream available pool.
	report.Mode, report.Records = "preview", nil
	saveReport(report, now.Add(time.Second))
	report.Mode, report.Error = "sync", "provider unavailable"
	saveReport(report, now.Add(2*time.Second))
	report.Error, report.Domain = "", "old.example"
	saveReport(report, now.Add(3*time.Second))
	checkPage("page=1", "28", 20)
	checkPage("page=2", "28", 8)
	checkPage("region=US", "6", 6)
	checkPage("search=8.8.4.29", "1", 1)
	pageRequest(t, a.generateIPLinks, "/available-ips/links?ip=8.8.4.1", 200)
	pageRequest(t, a.generateIPLinks, "/available-ips/links?ip=8.8.4.29", 200)
	pageRequest(t, a.generateIPLinks, "/available-ips/links?ip=8.8.4.26", 409)
	// DNS can narrow this pool, while website checks and existing failures stay separate.
	items := []dnsCandidate{
		{IP: "8.8.4.29", State: "healthy"},
		{IP: "8.8.4.26", State: "healthy"},
		{IP: "8.8.4.27", State: "healthy"},
		{IP: "8.8.4.26", Pool: "cf", State: "healthy"},
		{IP: "8.8.4.1", State: "failed"},
	}
	if err := a.filterDNSAvailableNodes(ctx, items); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"healthy", "unknown", "unknown", "healthy", "failed"} {
		if items[i].State != want {
			t.Fatalf("DNS gate item %d: %+v, want %s", i, items[i], want)
		}
	}
	// The full probe target pool must remain available to requalify unselected IPs.
	var targets int
	if err := a.db.QueryRow(ctx, "SELECT count(DISTINCT ip) FROM shared_node_ip_pool WHERE available").Scan(&targets); err != nil || targets != 29 {
		t.Fatalf("probe targets=%d err=%v", targets, err)
	}
	exec(`UPDATE dns_settings SET config=jsonb_set(config,'{enabled}','false')`)
	checkPage("", "28", 20)
}
