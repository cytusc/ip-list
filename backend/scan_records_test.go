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
