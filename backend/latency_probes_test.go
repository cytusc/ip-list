package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProbeTargetsDeduplicatesAndExcludesBlacklist(t *testing.T) {
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
	token, hash, err := newProbeToken()
	if err != nil {
		t.Fatal(err)
	}
	var id string
	err = db.QueryRow(ctx, "INSERT INTO latency_probes(name,token_hash) VALUES($1,$2) RETURNING id::text", "targets-"+time.Now().Format("150405.000000000"), hash).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, "DELETE FROM latency_probes WHERE id=$1", id)
	defer db.Exec(ctx, "DELETE FROM node_ip_pool WHERE protocol IN ('latency/test-a','latency/test-b')")
	defer db.Exec(ctx, "DELETE FROM ip_blacklist WHERE ip='198.18.99.3'")
	_, err = db.Exec(ctx, `INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at) VALUES
 ('latency/test-a','198.18.99.1',true,1,now()),
 ('latency/test-b','198.18.99.1',true,1,now()),
 ('latency/test-a','198.18.99.2',true,1,now()),
 ('latency/test-a','198.18.99.10',true,1,now()),
 ('latency/test-a','198.18.99.3',true,1,now()),
 ('latency/test-a','198.18.99.4',false,0,now());
 INSERT INTO ip_blacklist(ip) VALUES('198.18.99.3')`)
	if err != nil {
		t.Fatal(err)
	}
	app := &app{db: db}
	req := httptest.NewRequest(http.MethodGet, "/probe-agent/targets", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	app.probeAgentTargets(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("targets: HTTP %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		IPs []string `json:"ips"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	selected := []string{}
	for _, ip := range payload.IPs {
		if strings.HasPrefix(ip, "198.18.99.") {
			selected = append(selected, ip)
		}
	}
	if !reflect.DeepEqual(selected, []string{"198.18.99.1", "198.18.99.2", "198.18.99.10"}) {
		t.Fatalf("unexpected targets: %v", selected)
	}
	req.Header.Set("Authorization", "Bearer invalid-token")
	denied := httptest.NewRecorder()
	app.probeAgentTargets(denied, req)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request: HTTP %d", denied.Code)
	}
}

func TestProbeMonitoringStatus(t *testing.T) {
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
	token, hash, err := newProbeToken()
	if err != nil {
		t.Fatal(err)
	}
	var id string
	err = db.QueryRow(ctx, `INSERT INTO latency_probes(name,token_hash,interval_seconds,last_seen,heartbeat_at)
 VALUES('monitoring-test',$1,3600,now()-interval '2 minutes',now()-interval '2 minutes') RETURNING id::text`, hash).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, "DELETE FROM latency_probes WHERE id=$1", id)
	defer db.Exec(ctx, "DELETE FROM node_ip_pool WHERE protocol='latency/monitoring-test'")
	_, err = db.Exec(ctx, `INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at) VALUES('latency/monitoring-test','198.18.98.1',true,1,now());`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `INSERT INTO latency_probe_results(probe_id,ip,delay_ms,success,checked_at) VALUES($1,'198.18.98.1',42,true,now()-interval '1 day')`, id)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{db: db}
	getStatus := func() latencyProbe {
		t.Helper()
		w := httptest.NewRecorder()
		a.listLatencyProbes(w, httptest.NewRequest("GET", "/latency-probes", nil))
		var probes []latencyProbe
		if w.Code != 200 {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &probes); err != nil {
			t.Fatal(err)
		}
		for _, probe := range probes {
			if probe.ID == id {
				return probe
			}
		}
		t.Fatal("missing probe")
		return latencyProbe{}
	}
	if getStatus().Online {
		t.Fatal("stale heartbeat must be offline despite long interval")
	}
	w := httptest.NewRecorder()
	a.probeAgentHeartbeat(w, httptest.NewRequest("POST", "/probe-agent/heartbeat", nil))
	if w.Code != 401 {
		t.Fatal("heartbeat must require authentication")
	}
	req := httptest.NewRequest("POST", "/probe-agent/heartbeat", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	a.probeAgentHeartbeat(w, req)
	if w.Code != 200 || !getStatus().Online {
		t.Fatalf("fresh heartbeat must be online: %d", w.Code)
	}
	w = httptest.NewRecorder()
	a.availableIPs(w, httptest.NewRequest("GET", "/available-ips", nil))
	if w.Code != 200 {
		t.Fatalf("available IPs: %d %s", w.Code, w.Body.String())
	}
	var ips []availableIP
	if err := json.Unmarshal(w.Body.Bytes(), &ips); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ip := range ips {
		for _, delay := range ip.ProbeDelays {
			if ip.IP == "198.18.98.1" && delay.ProbeID == id {
				found = true
				if !delay.Online || delay.Fresh || delay.DelayMS == nil || *delay.DelayMS != 42 {
					t.Fatalf("online agent with historical data: %+v", delay)
				}
			}
		}
	}
	if !found {
		t.Fatal("missing delay")
	}
	// A recent successful sample is historical once its agent disconnects.
	_, err = db.Exec(ctx, "UPDATE latency_probe_results SET checked_at=now() WHERE probe_id=$1", id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, "UPDATE latency_probes SET last_seen=now()-interval '2 minutes' WHERE id=$1", id)
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	a.availableIPs(w, httptest.NewRequest("GET", "/available-ips", nil))
	if w.Code != 200 {
		t.Fatalf("available IPs: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ips); err != nil {
		t.Fatal(err)
	}
	for _, ip := range ips {
		for _, delay := range ip.ProbeDelays {
			if ip.IP == "198.18.98.1" && delay.ProbeID == id && (delay.Online || !delay.Fresh) {
				t.Fatalf("fresh sample must not imply online: %+v", delay)
			}
		}
	}
	// Legacy agents remain supported until they are upgraded to independent heartbeats.
	_, err = db.Exec(ctx, "UPDATE latency_probes SET heartbeat_at=NULL,last_seen=now()-interval '2 minutes' WHERE id=$1", id)
	if err != nil {
		t.Fatal(err)
	}
	if !getStatus().Online {
		t.Fatal("legacy interval-based status changed")
	}
}
