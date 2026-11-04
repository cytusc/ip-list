package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeCompactionPreservesHistory(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	var id string
	err := a.db.QueryRow(ctx, `INSERT INTO latency_probes(name,token_hash) VALUES('history','history') RETURNING id::text`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	fixture := `
 INSERT INTO latency_probe_results(probe_id,ip,delay_ms,success,checked_at)
 SELECT $1,'1.1.1.1',CASE WHEN n%2=0 THEN (n+1)*10 ELSE 0 END,n%2=0,
 date_bin('15 minutes',now()-interval '3 hours','2000-01-01'::timestamptz)+n*interval '20 seconds' FROM generate_series(0,5) n;
 INSERT INTO latency_probe_results(probe_id,ip,delay_ms,success,checked_at) VALUES($1,'1.1.1.1',80,true,now()-interval '10 minutes');
 INSERT INTO latency_probe_results(probe_id,ip,delay_ms,success,checked_at) VALUES($1,'1.1.1.1',10,true,now()-interval '25 hours');
 INSERT INTO latency_probe_results(probe_id,ip,mode,tcp_port,delay_ms,success,checked_at)
 VALUES($1,'1.1.1.1','tcp',443,999,true,now()-interval '3 hours');
 INSERT INTO latency_probes(name,token_hash,enabled) VALUES('disabled','disabled',false);
 INSERT INTO latency_probe_results(probe_id,ip,delay_ms,success,checked_at)
 SELECT id,'1.1.1.1',90,true,now()-interval '3 hours' FROM latency_probes WHERE name='disabled';`
	for _, statement := range strings.Split(fixture, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		var args []any
		if strings.Contains(statement, "$1") {
			args = []any{id}
		}
		if _, err = a.db.Exec(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	history := func() []latencyProbeResult {
		t.Helper()
		r := httptest.NewRequest("GET", "/history?ip=1.1.1.1&hours=24", nil)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		a.latencyProbeHistory(w, r)
		var result []latencyProbeResult
		if w.Code != 200 {
			t.Fatalf("history: %d %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	check := func(wantCount, wantOK int64, wantSum float64) {
		t.Helper()
		items := history()
		if len(items) != 2 {
			t.Fatalf("history=%+v", items)
		}
		h := items[0]
		if h.BucketSeconds != 900 || h.SampleCount != wantCount || h.SuccessCount != wantOK || h.DelaySum != wantSum || h.DelayMS != wantSum/float64(wantOK) || h.MinDelayMS == nil || *h.MinDelayMS != 10 || h.MaxDelayMS == nil || (wantCount == 6 && *h.MaxDelayMS != 50) || (wantCount == 7 && *h.MaxDelayMS != 100) {
			t.Fatalf("incorrect summary: %+v", h)
		}
		if items[1].BucketSeconds != 0 || items[1].DelayMS != 80 {
			t.Fatalf("recent sample changed: %+v", items[1])
		}
	}
	check(6, 3, 90)
	// Split one bucket across batches; readers see the same totals at every step.
	for {
		n, err := a.compactProbeSamples(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		check(6, 3, 90)
		if n == 0 {
			break
		}
	}
	var raw, rollups int
	a.db.QueryRow(ctx, `SELECT count(*) FROM latency_probe_results`).Scan(&raw)
	a.db.QueryRow(ctx, `SELECT count(*) FROM latency_probe_rollups`).Scan(&rollups)
	if raw != 1 || rollups != 3 {
		t.Fatalf("raw=%d rollups=%d", raw, rollups)
	}
	// Late data in an already compacted bucket is merged exactly once.
	_, err = a.db.Exec(ctx, `INSERT INTO latency_probe_results(probe_id,ip,delay_ms,success,checked_at)
 SELECT $1,'1.1.1.1',100,true,date_bin('15 minutes',now()-interval '3 hours','2000-01-01'::timestamptz)`, id)
	if err != nil {
		t.Fatal(err)
	}
	check(7, 4, 190)
	if _, err = a.compactProbeSamples(ctx, 20); err != nil {
		t.Fatal(err)
	}
	check(7, 4, 190)
	if n, err := a.compactProbeSamples(ctx, 20); err != nil || n != 0 {
		t.Fatalf("repeat: %d %v", n, err)
	}
	check(7, 4, 190)
	// Expire summaries even when the probe is disabled and no raw rows remain.
	_, err = a.db.Exec(ctx, `UPDATE latency_probe_rollups SET bucket_start=now()-interval '25 hours' WHERE probe_id<>$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.compactProbeSamples(ctx, 20); err != nil {
		t.Fatal(err)
	}
	a.db.QueryRow(ctx, `SELECT count(*) FROM latency_probe_rollups`).Scan(&rollups)
	if rollups != 2 {
		t.Fatalf("expired disabled summaries remain: %d", rollups)
	}
}

func TestProbeCompactionRollbackAndBoundary(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	_, err := a.db.Exec(ctx, `
 INSERT INTO latency_probes(name,token_hash) VALUES('rollback','rollback');
 INSERT INTO latency_probe_results(probe_id,ip,delay_ms,success,checked_at)
 SELECT id,'1.1.1.1',0,false,date_bin('15 minutes',now()-interval '2 hours','2000-01-01'::timestamptz)+age*interval '1 second'
 FROM latency_probes CROSS JOIN unnest(ARRAY[-1,0,1]) age;
 CREATE FUNCTION fail_rollup() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'simulate failed summary'; END $$;
 CREATE TRIGGER fail_rollup BEFORE INSERT ON latency_probe_rollups FOR EACH ROW EXECUTE FUNCTION fail_rollup();`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.compactProbeSamples(ctx, 20); err == nil {
		t.Fatal("expected summary failure")
	}
	var raw int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM latency_probe_results`).Scan(&raw); err != nil || raw != 3 {
		t.Fatalf("rollback lost raw samples: %d %v", raw, err)
	}
	if _, err = a.db.Exec(ctx, `DROP TRIGGER fail_rollup ON latency_probe_rollups`); err != nil {
		t.Fatal(err)
	}
	if n, err := a.compactProbeSamples(ctx, 20); err != nil || n != 1 {
		t.Fatalf("boundary: %d %v", n, err)
	}
	var total, ok int
	var low, high *float64
	if err = a.db.QueryRow(ctx, `SELECT sample_count,success_count,min_delay_ms,max_delay_ms FROM latency_probe_rollups`).Scan(&total, &ok, &low, &high); err != nil || total != 1 || ok != 0 || low != nil || high != nil {
		t.Fatalf("failed-only summary: %d %d %v %v %v", total, ok, low, high, err)
	}
}

func TestProbeCompactionRebinsExistingSummaries(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	var id string
	if err := a.db.QueryRow(ctx, `INSERT INTO latency_probes(name,token_hash) VALUES('rebucket','rebucket') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	_, err := a.db.Exec(ctx, `INSERT INTO latency_probe_rollups(probe_id,ip,mode,tcp_port,bucket_start,sample_count,success_count,delay_sum,min_delay_ms,max_delay_ms)
 SELECT $1,'1.1.1.1','icmp',0,date_bin('15 minutes',now()-interval '3 hours','2000-01-01'::timestamptz)+n*interval '5 minutes',15,10,100,5,20 FROM generate_series(0,2) n;
 `, id)
	if err != nil {
		t.Fatal(err)
	}
	// An expired summary on the same key is discarded, never added to the new day.
	_, err = a.db.Exec(ctx, `INSERT INTO latency_probe_rollups(probe_id,ip,mode,tcp_port,bucket_start,sample_count,success_count,delay_sum)
 VALUES($1,'1.1.1.1','icmp',0,now()-interval '25 hours',100,0,0)`, id)
	if err != nil {
		t.Fatal(err)
	}
	checkHistory := func() {
		t.Helper()
		var total, ok int64
		var sum float64
		query := "SELECT sum(sample_count),sum(success_count),sum(delay_sum) FROM (" + probeHistorySQL + ") h(id,ip,delay_ms,success,checked_at,bucket_seconds,sample_count,success_count,delay_sum,low,high)"
		if err := a.db.QueryRow(ctx, query, id, "1.1.1.1", 24).Scan(&total, &ok, &sum); err != nil || total != 45 || ok != 30 || sum != 300 {
			t.Fatalf("history changed: %d %d %v %v", total, ok, sum, err)
		}
	}
	checkHistory()
	for {
		n, err := a.compactProbeSamples(ctx, 1)
		checkHistory()
		if err != nil {
			t.Fatal(err)
		}
		var total, ok int64
		var sum float64
		if err := a.db.QueryRow(ctx, `SELECT sum(sample_count),sum(success_count),sum(delay_sum) FROM latency_probe_rollups WHERE bucket_start>now()-interval '24 hours'`).Scan(&total, &ok, &sum); err != nil || total != 45 || ok != 30 || sum != 300 {
			t.Fatalf("counts changed: %d %d %v %v", total, ok, sum, err)
		}
		if n == 0 {
			break
		}
	}
	var count int
	var low, high float64
	if err := a.db.QueryRow(ctx, `SELECT count(*),min(min_delay_ms),max(max_delay_ms) FROM latency_probe_rollups`).Scan(&count, &low, &high); err != nil || count != 1 || low != 5 || high != 20 {
		t.Fatalf("rebucket: %d %v %v %v", count, low, high, err)
	}
	if n, err := a.compactProbeSamples(ctx, 1); err != nil || n != 0 {
		t.Fatalf("repeat %d %v", n, err)
	}
}
