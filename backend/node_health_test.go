package main

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNodeHealthRequiresFreshCompleteEvidence(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := a.db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	const ip = "38.59.225.75"
	exec(`INSERT INTO nodes(name,address,port,share_uri) VALUES
 ('EUGame','gb.example',443,'vless://11111111-1111-4111-8111-111111111111@gb.example:443?type=grpc&security=tls');
 INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at,config_hash,certificate)
 SELECT 'grpc','38.59.225.75',true,20,now()-interval '1 day',md5(share_uri),'{"status":"passed"}' FROM nodes;
 INSERT INTO cf_ip_pool(ip,available,delay_ms,http_status,checked_at) VALUES('1.1.1.1',true,20,200,now());`)
	items := []dnsCandidate{{IP: ip, State: "healthy"}}
	hash := func() string {
		t.Helper()
		var h string
		if err := a.db.QueryRow(ctx, `SELECT md5(string_agg(config_hash,',' ORDER BY config_hash)) FROM node_grpc_samples`).Scan(&h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	check := func(want bool) {
		t.Helper()
		var visible bool
		if err := a.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM current_node_ip_pool WHERE available)").Scan(&visible); err != nil || visible != want {
			t.Fatalf("visible=%v want=%v err=%v", visible, want, err)
		}
		w := httptest.NewRecorder()
		a.generateIPLinks(w, httptest.NewRequest("GET", "/available-ips/links?ip="+ip, nil))
		if want && w.Code != 200 || !want && w.Code != 409 {
			t.Fatalf("links=%d want available=%v", w.Code, want)
		}
		w = httptest.NewRecorder()
		a.availableIPs(w, httptest.NewRequest("GET", "/available-ips", nil))
		if w.Code != 200 || (w.Body.String() == "[]\n") == want {
			t.Fatalf("available list=%d %s", w.Code, w.Body.String())
		}
		var websites int
		if err := a.db.QueryRow(ctx, "SELECT count(*) FROM cf_ip_pool WHERE available").Scan(&websites); err != nil || websites != 1 {
			t.Fatal("node checks changed website pool", websites, err)
		}
		// DNS still sees raw evidence so it can retry instead of treating expiry as deletion.
		candidates, _, err := a.dnsCandidates(ctx, dnsConfig{Window: 5})
		if err != nil || len(candidates) != 1 || candidates[0].IP != ip {
			t.Fatalf("lost DNS recheck candidate: %+v %v", candidates, err)
		}
	}
	save := func(status string, at time.Time) {
		t.Helper()
		if err := a.saveNodeHealth(ctx, hash(), at, items, []string{status}); err != nil {
			t.Fatal(err)
		}
	}
	check(false) // A successful old scan alone proves nothing about current availability.
	save("passed", time.Now().Add(-16*time.Minute))
	check(false)
	save("passed", time.Now())
	check(true)
	save("unknown", time.Now())
	check(false)
	save("failed", time.Now())
	check(false)
	if err := a.saveNodeHealth(ctx, hash(), time.Now().Add(-time.Minute), items, []string{"passed"}); err == nil {
		t.Fatal("older success accepted after newer failure")
	}
	check(false)
	save("passed", time.Now())
	check(true)
	oldHash := hash()
	exec(`INSERT INTO nodes(name,address,port,share_uri) VALUES('HK2','hk.example',443,'vless://11111111-1111-4111-8111-111111111111@hk.example:443?type=grpc&security=tls')`)
	check(false)
	if err := a.saveNodeHealth(ctx, oldHash, time.Now(), items, []string{"passed"}); err == nil {
		t.Fatal("old sample set published after configuration change")
	}
	save("passed", time.Now())
	check(true)
	exec(`UPDATE nodes SET share_uri=share_uri||'&serviceName=changed' WHERE name='HK2'`)
	check(false)
	save("passed", time.Now())
	check(true)
	exec(`UPDATE nodes SET enabled=false WHERE name='HK2'`)
	check(false)
}
