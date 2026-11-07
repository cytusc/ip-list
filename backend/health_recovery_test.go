package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestHealthRecoverySpacedRounds(t *testing.T) {
	at := time.Now()
	h := healthRecovery{}
	for _, minute := range []int{0, 0, 1, 2, 3, 4, 5, 6, 9} {
		if h.observe("passed", at.Add(time.Duration(minute)*time.Minute), true) {
			t.Fatal("rapid checks bypassed observation", minute, h)
		}
	}
	if !h.observe("passed", at.Add(10*time.Minute), true) {
		t.Fatal("three rounds did not recover", h)
	}
	h.observe("failed", at.Add(11*time.Minute), true)
	if h.observe("passed", at.Add(12*time.Minute), true) || !h.Quarantined {
		t.Fatal("one pass cleared failure", h)
	}
	h.observe("passed", at.Add(17*time.Minute), true)
	h.observe("unknown", at.Add(18*time.Minute), false)
	if h.observe("passed", at.Add(22*time.Minute), true) {
		t.Fatal("unknown did not break streak", h)
	}
	h.observe("passed", at.Add(27*time.Minute), true)
	if !h.observe("passed", at.Add(32*time.Minute), true) || h.Quarantined {
		t.Fatal("recovery failed", h)
	}
	if h.observe("passed", at.Add(48*time.Minute), true) || h.Rounds != 1 {
		t.Fatal("stale gap did not reset streak", h)
	}
}

func TestHealthUnknownExpiryNeedsControl(t *testing.T) {
	at := time.Now()
	h := healthRecovery{}
	h.observe("unknown", at, false)
	h.observe("unknown", at.Add(time.Hour), false)
	if h.Quarantined {
		t.Fatal("checker outage evicted IP")
	}
	h.observe("unknown", at.Add(time.Hour), true)
	if !h.Quarantined {
		t.Fatal("healthy control failed to bound retention")
	}
}

func TestHealthRecoveryPersistence(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	at := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Microsecond)
	save := func(pool, hash, status string, when time.Time) ([]dnsCandidate, error) {
		t.Helper()
		tx, err := a.db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		items := []dnsCandidate{{IP: "1.1.1.1", Pool: pool, State: "healthy", HealthControl: true}}
		err = saveHealthRecovery(ctx, tx, pool, hash, when, items, []string{status})
		if err == nil {
			err = tx.Commit(ctx)
		}
		return items, err
	}
	for i := 0; i < 3; i++ {
		items, err := save("node", "config", "passed", at.Add(time.Duration(i)*5*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if (items[0].State == "healthy") != (i == 2) {
			t.Fatal("unexpected admission", i, items)
		}
	}
	if _, err := save("node", "config", "failed", at.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := save("node", "config", "passed", at); err == nil {
		t.Fatal("stale check overwrote failure")
	}
	items, err := save("node", "config", "passed", at.Add(12*time.Minute))
	if err != nil || items[0].State != "pending" {
		t.Fatal(items, err)
	}
	items, err = save("cf", "business-url", "passed", at.Add(12*time.Minute))
	if err != nil || items[0].State != "unknown" {
		t.Fatal("pools not isolated", items, err)
	}
	items, err = save("node", "changed-config", "passed", at.Add(13*time.Minute))
	if err != nil || items[0].State != "unknown" {
		t.Fatal("config did not reset observation", items, err)
	}
	var total int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM ip_health_checks WHERE pool='node'`).Scan(&total); err != nil || total != 6 {
		t.Fatal("history missing or stale check persisted", total, err)
	}
	var raw []byte
	if err = a.db.QueryRow(ctx, `SELECT state FROM ip_health_recovery WHERE pool='node'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var h healthRecovery
	if err = json.Unmarshal(raw, &h); err != nil || h.Rounds != 1 {
		t.Fatal(h, err)
	}
}

func TestProxySampleCoverageAndPolicyMigration(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	_, err := a.db.Exec(ctx, `INSERT INTO nodes(name,address,port,share_uri)
 SELECT transport,'node.example',443,'vless://credential@node.example:443?security=tls&type='||transport
 FROM unnest(ARRAY['ws','grpc','xhttp','httpupgrade','tcp']) transport;
 INSERT INTO nodes(name,address,port,share_uri) VALUES
 ('plain','node.example',443,'vless://credential@node.example:443?security=none&type=ws'),
 ('reality','node.example',443,'vless://credential@node.example:443?security=reality&type=grpc'),
 ('hy2','node.example',443,'hysteria2://credential@node.example:443'),
 ('tuic','node.example',443,'tuic://credential@node.example:443'),
 ('trojan','node.example',443,'trojan://credential@node.example:443?type=ws');
 UPDATE dns_settings SET config=jsonb_set(config,'{min_success_percent}','0');`)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM node_proxy_samples`).Scan(&count); err != nil || count != 5 {
		t.Fatal(count, err)
	}
	if _, err = a.db.Exec(ctx, schema); err != nil {
		t.Fatal("schema rerun failed", err)
	}
	if err = a.db.QueryRow(ctx, `SELECT (config->>'min_success_percent')::int FROM dns_settings`).Scan(&count); err != nil || count != 90 {
		t.Fatal("legacy disabled threshold was not migrated", count, err)
	}
}

func TestHealthFlappingDoesNotResetUnknownLimit(t *testing.T) {
	at := time.Now()
	h := healthRecovery{}
	h.observe("unknown", at, true)
	for i := 1; i <= 29; i++ {
		status := "unknown"
		if i%2 == 0 {
			status = "passed"
		}
		h.observe(status, at.Add(time.Duration(i)*time.Minute), true)
	}
	h.observe("unknown", at.Add(30*time.Minute), true)
	if !h.Quarantined {
		t.Fatal("isolated passes indefinitely retained an unstable IP", h)
	}
}

func TestDNSSuccessRatioCountsFailuresBeforeTrimming(t *testing.T) {
	at := time.Now()
	c := dnsTestConfig()
	c.MinSuccess = 90
	samples := make([]dnsSample, 10)
	for i := range samples {
		samples[i] = dnsSample{At: at.Add(time.Duration(i-9) * 20 * time.Second), Success: i != 0, Delay: 20}
	}
	if state, _ := dnsProbeHealth(samples, at, time.Minute, c); state != "healthy" {
		t.Fatal("90% boundary rejected", state)
	}
	samples[1].Success = false
	if state, _ := dnsProbeHealth(samples, at, time.Minute, c); state != "pending" {
		t.Fatal("loss ignored", state)
	}
	if dnsCorroborateFailure("pending", false) != "unknown" {
		t.Fatal("checker outage evicted IP")
	}
	c.MinSuccess = 0
	if c.validate() == nil {
		t.Fatal("disabled success gate allowed")
	}
}
