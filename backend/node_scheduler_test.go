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

func TestNodeScheduleResumeAndStop(t *testing.T) {
	databaseURL, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if databaseURL == "" || redisURL == "" {
		t.Skip("requires test DB and Redis")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := pgxpool.New(ctx, databaseURL)
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
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(options)
	defer rdb.Close()
	a := &app{db: db, redis: rdb, scanContext: ctx}
	id := fmt.Sprintf("%032x", time.Now().UnixNano())
	nodeID, networkID := "", ""
	raw := "vless://" + testUUID + "@sample.invalid:1?security=none&type=grpc"
	if err = db.QueryRow(ctx, "INSERT INTO nodes(name,address,port,share_uri) VALUES($1,'sample.invalid',1,$2) RETURNING id::text", "schedule-"+id, raw).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, "INSERT INTO networks(name,cidr) VALUES($1,'127.0.0.2/32') RETURNING id::text", "schedule-"+id).Scan(&networkID); err != nil {
		t.Fatal(err)
	}
	job := scanJob{ID: id, UserID: "schedule-test", Protocol: connectivityProtocol, Status: "running", Round: 1, Input: scanInput{NodeID: nodeID, NetworkIDs: []string{networkID}, URL: "http://probe.invalid/", Timeout: 1000, Concurrency: 1, IntervalMinutes: 1, FullEvery: 12}}
	defer func() {
		cancel()
		deadline := time.Now().Add(4 * time.Second)
		for rdb.Get(context.Background(), job.lockKey()).Val() == id && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		db.Exec(context.Background(), "DELETE FROM node_scan_schedules WHERE protocol=$1 AND job_id=$2", job.Protocol, id)
		db.Exec(context.Background(), "DELETE FROM networks WHERE id=$1", networkID)
		db.Exec(context.Background(), "DELETE FROM nodes WHERE id=$1", nodeID)
		db.Exec(context.Background(), "DELETE FROM node_ip_pool WHERE protocol=$1 AND ip='127.0.0.2'", job.Protocol)
		rdb.Del(context.Background(), scanKey(id), scanKey(id)+":cancel", latestKey(job.UserID), latestKey(job.UserID)+":"+job.Protocol)
	}()
	data, _ := json.Marshal(job)
	if _, err = db.Exec(ctx, "INSERT INTO node_scan_schedules(protocol,job_id,job) VALUES($1,$2,$3)", job.Protocol, id, data); err != nil {
		t.Fatal(err)
	}
	if err = a.resumeDueNodeSchedules(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.resumeDueNodeSchedules(ctx); err != nil {
		t.Fatal(err)
	}
	var current scanJob
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err = rdb.Get(ctx, scanKey(id)).Bytes()
		if err == nil && json.Unmarshal(data, &current) == nil && current.Status == "waiting" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if current.Round != 2 || current.Status != "waiting" || current.Completed != 1 {
		t.Fatalf("resume failed: %+v", current)
	}
	if current.Input.NodeID != nodeID {
		t.Fatal("sample missing")
	}
	// The database snapshot survives Redis loss and retains scheduling configuration.
	var persisted scanJob
	if err = db.QueryRow(ctx, "SELECT job FROM node_scan_schedules WHERE protocol=$1", job.Protocol).Scan(&data); err != nil || json.Unmarshal(data, &persisted) != nil || persisted.Status != "waiting" {
		t.Fatal("schedule snapshot missing", err)
	}
	request := httptest.NewRequest("POST", "/scans/"+id+"/cancel", nil).WithContext(context.WithValue(ctx, userContextKey{}, user{ID: job.UserID}))
	request.SetPathValue("id", id)
	w := httptest.NewRecorder()
	a.cancelScan(w, request)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var enabled bool
	if err = db.QueryRow(ctx, "SELECT enabled FROM node_scan_schedules WHERE protocol=$1", job.Protocol).Scan(&enabled); err != nil || enabled {
		t.Fatal("explicit stop did not disable schedule", err)
	}
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && rdb.Get(ctx, job.lockKey()).Val() == id {
		time.Sleep(20 * time.Millisecond)
	}
	if rdb.Get(ctx, job.lockKey()).Val() == id {
		t.Fatal("stop did not release protocol lease")
	}
	if err = a.resumeDueNodeSchedules(ctx); err != nil {
		t.Fatal(err)
	}
	if rdb.Get(ctx, job.lockKey()).Val() == id {
		t.Fatal("stopped schedule resumed")
	}
}
