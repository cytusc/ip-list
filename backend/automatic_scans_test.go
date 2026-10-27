package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func TestAutomaticNetworkScans(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("设置 TEST_DATABASE_URL 和 TEST_REDIS_URL 可验证自动增量检测")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !strings.HasSuffix(db.Config().ConnConfig.Database, "_test") {
		t.Fatal("测试数据库名必须以 _test 结尾")
	}
	options, err := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(options)
	defer rdb.Close()
	if _, err = db.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, redis: rdb, scanContext: ctx}
	suffix := fmt.Sprint(time.Now().UnixNano())
	var userID, nodeID, targetID string
	if err = db.QueryRow(ctx, "INSERT INTO users(username,password_hash) VALUES($1,'test') RETURNING id::text", "auto"+suffix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.Exec(context.Background(), "DELETE FROM users WHERE id=$1", userID) }()
	raw := "vless://11111111-1111-4111-8111-111111111111@127.0.0.1:1?encryption=none&type=tcp&security=none#auto"
	if err = db.QueryRow(ctx, "INSERT INTO nodes(name,address,port,share_uri) VALUES($1,'127.0.0.1',1,$2) RETURNING id::text", "auto"+suffix, raw).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.Exec(context.Background(), "DELETE FROM nodes WHERE id=$1", nodeID) }()
	if err = db.QueryRow(ctx, "INSERT INTO cf_targets(name,url) VALUES($1,$2) RETURNING id::text", "auto"+suffix, "https://127.0.0.1:1/"+suffix).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.Exec(context.Background(), "DELETE FROM cf_targets WHERE id=$1", targetID) }()
	protocol := nodePoolProtocol(raw)
	old := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if _, err = db.Exec(ctx, "INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at) VALUES($1,'127.0.0.2',true,42,$2) ON CONFLICT(protocol,ip) DO UPDATE SET available=true,delay_ms=42,checked_at=$2", protocol, old); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, "UPDATE node_ip_pool SET config_hash=md5($1),certificate='{\"status\":\"passed\",\"server_name\":\"sample.invalid\"}'::jsonb WHERE protocol=$2 AND ip='127.0.0.2'", raw, protocol); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM node_ip_pool WHERE protocol=$1 AND ip <<= '127.0.0.0/30'::cidr", protocol)
	}()
	defer func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM cf_ip_pool WHERE ip <<= '127.0.0.0/30'::cidr")
	}()
	input := scanInput{NodeID: nodeID, URL: "http://127.0.0.1:1/", Timeout: 1000, Concurrency: 2}
	inputJSON, _ := json.Marshal(input)
	if _, err = db.Exec(ctx, `INSERT INTO node_scan_rounds(job_id,round,protocol,input,started_at,status) VALUES($1,1,$2,$3,now(),'completed')`, suffix, protocol, inputJSON); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM node_scan_rounds WHERE job_id=$1 OR (protocol=$2 AND node_name=$3)", suffix, protocol, "auto"+suffix)
	}()
	cfJob := scanJob{ID: "cf-auto" + suffix, TargetID: targetID, Input: scanInput{Timeout: 1000, Concurrency: 2}, UserID: userID}
	snapshot, _ := json.Marshal(cfJob)
	rdb.Set(ctx, scanKey(cfJob.ID), snapshot, scanTTL)
	rdb.Set(ctx, cfLatestKey, cfJob.ID, scanTTL)
	save := func(id string, enabled bool) string {
		t.Helper()
		body := fmt.Sprintf(`{"name":"auto%s","cidr":"127.0.0.0/30","enabled":%t}`, suffix, enabled)
		req := httptest.NewRequest("POST", "/networks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(ctx, userContextKey{}, user{ID: userID}))
		w := httptest.NewRecorder()
		a.save(w, req, "networks", id)
		if w.Code != 200 && w.Code != 201 {
			t.Fatalf("save: %d %s", w.Code, w.Body.String())
		}
		var response struct {
			ID string `json:"id"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.ID
	}
	networkID := save("", true)
	defer func() { _, _ = db.Exec(context.Background(), "DELETE FROM networks WHERE id=$1", networkID) }()
	count := func() int {
		t.Helper()
		var n int
		if err = db.QueryRow(ctx, "SELECT count(*) FROM network_auto_scans WHERE network_id=$1", networkID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 2 {
		t.Fatalf("expected node and CF queue entries, got %d", n)
	}
	// Busy pools retain the queue. A waiting patrol can lend its lease without
	// either losing it or changing the patrol schedule.
	lock := scanLock + ":" + protocol
	if err = rdb.Set(ctx, lock, "patrol-owner", 45*time.Second).Err(); err != nil {
		t.Fatal(err)
	}
	defer rdb.Del(context.Background(), lock)
	if worked, err := a.processAutomaticScan(ctx, protocol, ""); err != nil || worked {
		t.Fatalf("busy pool: %v %v", worked, err)
	}
	if worked, err := a.processAutomaticScan(ctx, protocol, "patrol-owner"); err != nil || !worked {
		t.Fatalf("borrowed lease: %v %v", worked, err)
	}
	if owner := rdb.Get(ctx, lock).Val(); owner != "patrol-owner" {
		t.Fatalf("lease lost: %q", owner)
	}
	// The borrowed worker must renew the patrol's lease and honor both its
	// own stop flag and the patrol's stop flag.
	borrowed := scanJob{ID: "borrowed-" + suffix, LeaseID: "patrol-owner", Protocol: protocol}
	keys := []string{lock, scanKey(borrowed.ID) + ":cancel", scanKey(borrowed.leaseID()) + ":cancel"}
	for _, key := range keys[1:] {
		if err = rdb.Set(ctx, key, "1", time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
		control, err := rdb.Eval(ctx, scanControlScript, keys, borrowed.leaseID()).Int()
		if err != nil || control != 1 {
			t.Fatalf("borrowed stop: %d %v", control, err)
		}
		rdb.Del(ctx, key)
	}
	control, err := rdb.Eval(ctx, scanControlScript, keys, borrowed.ID).Int()
	if err != nil || control != -1 {
		t.Fatalf("wrong lease owner: %d %v", control, err)
	}
	var available bool
	var delay int
	var checked time.Time
	if err = db.QueryRow(ctx, "SELECT available,delay_ms,checked_at FROM node_ip_pool WHERE protocol=$1 AND ip='127.0.0.2'", protocol).Scan(&available, &delay, &checked); err != nil {
		t.Fatal(err)
	}
	if !available || delay != 42 || !checked.Equal(old) {
		t.Fatalf("old result changed: %v %d %v", available, delay, checked)
	}
	// Certificate failures are completed probes, even while quarantined.
	// The next tick must drain this range instead of probing it forever.
	if _, err = db.Exec(ctx, `UPDATE node_ip_pool
 SET certificate='{"status":"invalid","server_name":"sample.invalid","error":"hostname mismatch"}'::jsonb
 WHERE protocol=$1 AND ip='127.0.0.0'`, protocol); err != nil {
		t.Fatal(err)
	}
	if worked, err := a.processAutomaticScan(ctx, protocol, "patrol-owner"); err != nil || worked {
		t.Fatalf("completed certificate failure was scanned again: worked=%v err=%v", worked, err)
	}
	if n := count(); n != 1 {
		t.Fatalf("completed node queue not drained: %d", n)
	}
	rdb.Del(ctx, lock)
	// Starting the background worker alone is enough to detect the queued CF
	// range; no start-scan request is sent.
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	workerApp := &app{db: db, redis: rdb, scanContext: workerCtx}
	go func() { defer close(workerDone); workerApp.maintainAutomaticScans(workerCtx) }()
	defer func() { stopWorker(); <-workerDone }()
	deadline := time.Now().Add(12 * time.Second)
	for count() != 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if count() != 0 {
		t.Fatal("background worker did not drain the queue")
	}
	var total int
	if err = db.QueryRow(ctx, "SELECT count(*) FROM cf_ip_pool WHERE ip <<= '127.0.0.0/30'::cidr").Scan(&total); err != nil || total != 4 {
		t.Fatalf("automatic CF results: %d %v", total, err)
	}
	// Editing a label or metadata must not enqueue another scan. Enabling a
	// previously disabled range does enqueue it, and deleting it removes work.
	save(networkID, true)
	if count() != 0 {
		t.Fatal("unchanged enabled range enqueued again")
	}
	save(networkID, false)
	if count() != 0 {
		t.Fatal("disabled range enqueued")
	}
	save(networkID, true)
	if count() != 2 {
		t.Fatal("re-enabled range not queued")
	}
	if _, err = db.Exec(ctx, "DELETE FROM networks WHERE id=$1", networkID); err != nil {
		t.Fatal(err)
	}
	if count() != 0 {
		t.Fatal("queue did not cascade on deletion")
	}
}

func TestAutomaticInputDefaults(t *testing.T) {
	input := scanInput{Timeout: -1, Concurrency: 99}
	normalizeAutomaticInput(&input)
	if input.Timeout != 5000 || input.Concurrency != 5 || input.URL == "" {
		t.Fatal(input)
	}
	valid := scanInput{Timeout: 1000, Concurrency: 32, URL: "https://example.com/check"}
	input = valid
	normalizeAutomaticInput(&input)
	if input.Timeout != valid.Timeout || input.Concurrency != valid.Concurrency || input.URL != valid.URL {
		t.Fatal("valid settings changed")
	}
}
