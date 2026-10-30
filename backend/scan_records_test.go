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
)

// A round is listed before it ends and keeps its results afterwards, so the
// list can show live progress and the record can still be reopened.
func TestScanRecords(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("设置 TEST_DATABASE_URL 可验证检测记录")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !strings.HasSuffix(db.Config().ConnConfig.Database, "_test") {
		t.Fatal("测试数据库名必须以 _test 结尾")
	}
	if _, err = db.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db}
	jobID := fmt.Sprintf("record%d", time.Now().UnixNano())
	// The test database is shared, so every record of this test uses one pool.
	protocol := fmt.Sprintf("vless/r%d", time.Now().UnixNano()%100000000)
	defer func() { _, _ = db.Exec(ctx, "DELETE FROM node_scan_rounds WHERE job_id=$1", jobID) }()
	now := time.Now().UTC()
	job := scanJob{
		ID: jobID, Protocol: protocol, NodeName: "记录节点", Round: 1, Scope: "full",
		URL: "http://probe.example/204", CIDRs: []string{"192.0.2.0/30"}, StartedAt: now,
		Status: "running", Total: 4, Input: scanInput{URL: "http://probe.example/204", Timeout: 5000, Concurrency: 2},
	}
	list := func() scanRecordList {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/scan-records?protocol="+protocol, nil)
		r = r.WithContext(context.WithValue(r.Context(), userContextKey{}, user{ID: "tester"}))
		a.listScanRecords(w, r)
		if w.Code != 200 {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		var result scanRecordList
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	detail := func(id int64) scanRecord {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/scan-records/1", nil)
		r.SetPathValue("id", fmt.Sprint(id))
		r = r.WithContext(context.WithValue(r.Context(), userContextKey{}, user{ID: "tester"}))
		a.getScanRecord(w, r)
		if w.Code != 200 {
			t.Fatalf("detail: %d %s", w.Code, w.Body.String())
		}
		var result scanRecord
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	if err = a.saveScanRecord(ctx, &job, nil); err != nil {
		t.Fatal(err)
	}
	items := list().Items
	if len(items) != 1 || items[0].Status != "running" || items[0].NodeName != "记录节点" ||
		items[0].Input.Timeout != 5000 || len(items[0].CIDRs) != 1 || len(items[0].Results) != 0 {
		t.Fatalf("running round: %+v", items)
	}
	recordID := items[0].ID

	// Progress heartbeats refresh the counters without touching stored results.
	job.Completed, job.Success = 2, 1
	if err = a.saveScanRecord(ctx, &job, nil); err != nil {
		t.Fatal(err)
	}
	if got := list().Items[0]; got.Completed != 2 || got.Success != 1 {
		t.Fatalf("heartbeat not saved: %+v", got)
	}

	results, err := json.Marshal([]scanResult{
		{IP: "192.0.2.1", Status: "success", Delay: 30},
		{IP: "192.0.2.2", Status: "failed", Error: "连接或代理请求超时"},
	})
	if err != nil {
		t.Fatal(err)
	}
	finished := now.Add(time.Second)
	job.Status, job.Completed, job.Success, job.FinishedAt = "completed", 4, 1, &finished
	job.Results = []scanResult{{IP: "192.0.2.1", Status: "success", Delay: 30}}
	if err = a.saveScanRecord(ctx, &job, results); err != nil {
		t.Fatal(err)
	}
	// The final heartbeat of a finished round must keep the stored results.
	if err = a.saveScanRecord(ctx, &job, nil); err != nil {
		t.Fatal(err)
	}
	got := detail(recordID)
	if got.Status != "completed" || len(got.Results) != 2 || got.Results[0].IP != "192.0.2.1" {
		t.Fatalf("stored results: %+v", got)
	}

	// A round without heartbeats was interrupted with the process.
	if _, err = db.Exec(ctx, "UPDATE node_scan_rounds SET status='running',updated_at=now()-interval '2 minutes' WHERE job_id=$1", jobID); err != nil {
		t.Fatal(err)
	}
	if status := list().Items[0].Status; status != "interrupted" {
		t.Fatalf("stale round status: %s", status)
	}

	// Waiting between patrol rounds is not a round of its own, and CF tasks
	// keep their own progress page.
	job.Status = "waiting"
	if err = a.saveScanRecord(ctx, &job, nil); err != nil {
		t.Fatal(err)
	}
	cf := job
	cf.Status, cf.TargetID = "running", "11111111-1111-4111-8111-111111111111"
	if err = a.saveScanRecord(ctx, &cf, nil); err != nil {
		t.Fatal(err)
	}
	if count := len(list().Items); count != 1 {
		t.Fatalf("unexpected records: %d", count)
	}

	// Every patrol round is its own record.
	job.Round, job.Status, job.FinishedAt = 2, "completed", &finished
	job.StartedAt = finished
	if err = a.saveScanRecord(ctx, &job, nil); err != nil {
		t.Fatal(err)
	}
	if items = list().Items; len(items) != 2 || items[0].Round != 2 {
		t.Fatalf("second round: %+v", items)
	}
}

// Counters use durable IP coverage, including failures and blacklist entries,
// rather than completed batch records or the current page of history.
func TestScanNetworkProgress(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("设置 TEST_DATABASE_URL 可验证网段总体进度")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !strings.HasSuffix(db.Config().ConnConfig.Database, "_test") {
		t.Fatal("测试数据库名必须以 _test 结尾")
	}
	if _, err = db.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db}
	baseline, err := a.scanNetworkProgress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cidr := fmt.Sprintf("fd91:%x:%x::/126", time.Now().UnixNano()&65535, (time.Now().UnixNano()>>16)&65535)
	var id, uid string
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	err = db.QueryRow(ctx, `INSERT INTO users(username,password_hash) VALUES($1,'unused') RETURNING id::text`, fmt.Sprintf("progress%d", time.Now().UnixNano())).Scan(&uid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.Exec(ctx, "DELETE FROM users WHERE id=$1", uid) }()
	err = db.QueryRow(ctx, `INSERT INTO networks(name,cidr,provider,asn) VALUES('进度测试',$1::cidr,'测试运营商','AS64512') RETURNING id::text`, cidr).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(ctx, "DELETE FROM networks WHERE id=$1", id)
		_, _ = db.Exec(ctx, "DELETE FROM node_ip_pool WHERE ip <<= $1::cidr", cidr)
		_, _ = db.Exec(ctx, "DELETE FROM ip_blacklist WHERE ip <<= $1::cidr", cidr)
	}()
	check := func(completed, pending int) {
		t.Helper()
		got, err := a.scanNetworkProgress(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want := networkScanProgress{Total: baseline.Total + 1, Completed: baseline.Completed + completed, Pending: baseline.Pending + pending}
		if got != want {
			t.Fatalf("progress=%+v, want %+v", got, want)
		}
	}
	check(0, 1) // A missing queue entry does not mean completed.
	exec(`INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at) SELECT $1,network($2::cidr)::inet+g,false,0,now() FROM generate_series(0,1) g`, connectivityProtocol, cidr)
	check(0, 1) // A finished batch or partial scan is still pending.
	exec(`INSERT INTO ip_blacklist(ip) SELECT network($1::cidr)::inet+g FROM generate_series(1,3) g`, cidr)
	check(1, 0) // Overlapping blacklist and results count each IP once.
	exec(`INSERT INTO network_auto_scans(network_id,cidr,protocol,user_id) VALUES($1,$2::cidr,$3,$4)`, id, cidr, connectivityProtocol, uid)
	check(0, 1) // A full recheck stays pending despite historical coverage.
	metadata, err := a.scanNetworkMetadata(ctx, []string{cidr})
	if err != nil || len(metadata) != 1 || metadata[0].Provider != "测试运营商" || metadata[0].ASN != "AS64512" {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
	exec("UPDATE networks SET enabled=false WHERE id=$1", id)
	got, err := a.scanNetworkProgress(ctx)
	if err != nil || got != baseline {
		t.Fatalf("disabled range counted: %+v err=%v", got, err)
	}
}
