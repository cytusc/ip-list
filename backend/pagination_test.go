package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func paginationApp(t *testing.T) *app {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires TEST_DATABASE_URL")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatal("requires test database")
	}
	ctx := context.Background()
	admin, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("pagination_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = name
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); admin.Exec(ctx, "DROP SCHEMA "+name+" CASCADE"); admin.Close() })
	if _, err = db.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	return &app{db: db}
}

func pageRequest(t *testing.T, handler http.HandlerFunc, path string, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r = r.WithContext(context.WithValue(r.Context(), userContextKey{}, user{ID: "00000000-0000-4000-8000-000000000001"}))
	w := httptest.NewRecorder()
	handler(w, r)
	if w.Code != want {
		t.Fatalf("%s: status %d: %s", path, w.Code, w.Body.String())
	}
	return w
}

func TestConfigurationPagination(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	for _, kind := range []string{"nodes", "networks"} {
		t.Run(kind, func(t *testing.T) {
			var sql string
			if kind == "nodes" {
				sql = `INSERT INTO nodes(name,address,port,enabled,created_at) SELECT 'page-'||i,'host-'||i,443,i%2=0,'2026-01-01'::timestamptz FROM generate_series(1,107) i`
			} else {
				sql = `INSERT INTO networks(name,cidr,enabled,created_at) SELECT 'page-'||i,('198.18.'||i||'.0/24')::cidr,i%2=0,'2026-01-01'::timestamptz FROM generate_series(1,107) i`
			}
			if _, err := a.db.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			handler := func(w http.ResponseWriter, r *http.Request) { a.list(w, r, kind) }
			seen := map[string]bool{}
			for page, want := range []int{50, 50, 7} {
				w := pageRequest(t, handler, fmt.Sprintf("/%s?page=%d", kind, page+1), 200)
				var rows []struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
					t.Fatal(err)
				}
				if len(rows) != want || w.Header().Get("X-Total-Count") != "107" || w.Header().Get("X-Enabled-Count") != "53" {
					t.Fatalf("wrong page: %d %v", len(rows), w.Header())
				}
				for _, row := range rows {
					if seen[row.ID] {
						t.Fatal("duplicate across pages")
					}
					seen[row.ID] = true
				}
			}
			w := pageRequest(t, handler, "/"+kind+"?page=999&search=page-107", 200)
			if w.Header().Get("X-Total-Count") != "1" || w.Header().Get("X-Page") != "1" {
				t.Fatal(w.Header())
			}
			var rows []struct {
				ID string `json:"id"`
			}
			json.Unmarshal(w.Body.Bytes(), &rows)
			w = pageRequest(t, handler, "/"+kind+"?ids="+rows[0].ID, 200)
			if w.Header().Get("X-Total-Count") != "1" {
				t.Fatal("selected record missing")
			}
			w = pageRequest(t, handler, "/"+kind+"?status=disabled", 200)
			if w.Header().Get("X-Total-Count") != "54" {
				t.Fatal(w.Header())
			}
			for _, page := range []string{"0", "-1", "invalid", "9999999999999999999999"} {
				pageRequest(t, handler, "/"+kind+"?page="+page, 422)
			}
		})
	}
}

func TestIPListPagination(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	statements := []string{
		`INSERT INTO nodes(name,address,port,share_uri) VALUES('pagination','host',443,'pagination-config')`,
		`INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at,config_hash,certificate) SELECT p,('198.18.0.'||i)::inet,true,20,now(),md5('pagination-config'),'{"status":"passed"}'::jsonb FROM generate_series(1,45) i CROSS JOIN unnest(ARRAY['vless/ws','vless/grpc']) p`,
		`INSERT INTO networks(name,cidr,asn,provider) VALUES('pagination','198.18.0.0/24','AS13335','Example')`,
		`INSERT INTO dns_ip_locations(ip,country,checked_at) SELECT ('198.18.0.'||i)::inet,'JP',now() FROM generate_series(1,45) i`,
		`INSERT INTO ip_blacklist(ip) VALUES('198.18.0.1')`,
		`INSERT INTO cf_ip_pool(ip,available,delay_ms,http_status,checked_at) SELECT ('198.18.0.'||i)::inet,true,20,200,now() FROM generate_series(1,45) i`,
	}
	for _, sql := range statements {
		if _, err := a.db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range []struct {
		name    string
		handler http.HandlerFunc
	}{{"available", a.availableIPs}, {"cf-pool", a.cfIPPool}} {
		t.Run(entry.name, func(t *testing.T) {
			seen := map[string]bool{}
			for page, want := range []int{20, 20, 4} {
				w := pageRequest(t, entry.handler, fmt.Sprintf("/list?page=%d", page+1), 200)
				var rows []availableIP
				if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
					t.Fatal(err)
				}
				if len(rows) != want || w.Header().Get("X-Total-Count") != "44" {
					t.Fatalf("wrong page %d %v %s", len(rows), w.Header(), w.Body.String())
				}
				for _, row := range rows {
					if row.IP == "198.18.0.1" || seen[row.IP] {
						t.Fatal("blacklist or duplicate")
					}
					seen[row.IP] = true
					if entry.name == "available" && len(row.Checks) != 2 {
						t.Fatal("protocol checks split across pages")
					}
				}
			}
			w := pageRequest(t, entry.handler, "/list?search=198.18.0.45&page=900", 200)
			if w.Header().Get("X-Total-Count") != "1" || w.Header().Get("X-Page") != "1" {
				t.Fatal(w.Header())
			}
		})
	}
	for _, query := range []string{"region=JP&provider=provider:Example&protocol=vless/ws", "region=US", "provider=missing"} {
		w := pageRequest(t, a.availableIPs, "/available-ips?"+query, 200)
		want := "0"
		if strings.HasPrefix(query, "region=JP") {
			want = "44"
		}
		if w.Header().Get("X-Total-Count") != want {
			t.Fatalf("filter %s: %v", query, w.Header())
		}
	}
}

func TestOtherListPagination(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO latency_probes(name,token_hash) SELECT 'probe-'||i,decode(md5(i::text),'hex') FROM generate_series(1,55) i`,
		`INSERT INTO cf_targets(name,url) SELECT 'target-'||i,'https://example.com/'||i FROM generate_series(1,55) i`,
		`INSERT INTO ip_blacklist(ip) SELECT ('198.18.0.'||i)::inet FROM generate_series(1,55) i`,
		`INSERT INTO node_scan_rounds(job_id,round,protocol,started_at,status) SELECT 'job-'||i,1,CASE WHEN i%2=0 THEN 'vless/ws' ELSE 'vless/grpc' END,now(),'completed' FROM generate_series(1,55) i`,
	} {
		if _, err := a.db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	for _, handler := range []http.HandlerFunc{a.listLatencyProbes, a.listCFTargets, a.listBlacklist, a.listScanRecords} {
		w := pageRequest(t, handler, "/list?page=2", 200)
		var raw any
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		var rows []any
		if list, ok := raw.([]any); ok {
			rows = list
		} else {
			rows = raw.(map[string]any)["items"].([]any)
		}
		if len(rows) != 5 || w.Header().Get("X-Total-Count") != "55" {
			t.Fatalf("wrong page %d %v", len(rows), w.Header())
		}
	}
	w := pageRequest(t, a.listLatencyProbes, "/latency-probes?search=probe-55", 200)
	if w.Header().Get("X-Total-Count") != "1" {
		t.Fatal("probe search failed")
	}
	w = pageRequest(t, a.listScanRecords, "/scan-records?protocol=vless/ws", 200)
	if w.Header().Get("X-Total-Count") != "27" {
		t.Fatal("protocol filter failed")
	}
}

func TestScanDetailPagination(t *testing.T) {
	a := paginationApp(t)
	var id string
	err := a.db.QueryRow(context.Background(), `INSERT INTO node_scan_rounds(job_id,round,protocol,started_at,status,results) SELECT 'detail',1,'vless/ws',now(),'completed',jsonb_agg(jsonb_build_object('ip','198.18.0.'||i,'status',CASE WHEN i%2=0 THEN 'success' ELSE 'failed' END,'delay_ms',i)) FROM generate_series(1,125) i RETURNING id::text`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	handler := func(w http.ResponseWriter, r *http.Request) { r.SetPathValue("id", id); a.getScanRecord(w, r) }
	for page, want := range []int{50, 50, 25} {
		w := pageRequest(t, handler, fmt.Sprintf("/scan-records/%s?paged=1&page=%d", id, page+1), 200)
		var item scanRecord
		if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		if len(item.Results) != want || w.Header().Get("X-Total-Count") != "125" {
			t.Fatalf("%d %v", len(item.Results), w.Header())
		}
	}
	w := pageRequest(t, handler, "/detail?paged=1&filter=success&page=2", 200)
	var item scanRecord
	json.Unmarshal(w.Body.Bytes(), &item)
	if len(item.Results) != 12 || w.Header().Get("X-Total-Count") != "62" {
		t.Fatalf("wrong filtered page: %s", w.Body.String())
	}
	for _, row := range item.Results {
		if row.Status != "success" {
			t.Fatal("wrong status")
		}
	}
	pageRequest(t, handler, "/detail?paged=1&page=0", 422)
	pageRequest(t, handler, "/detail?paged=1&filter=bad", 422)
}

func TestDNSListPagination(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	_, err := a.db.Exec(ctx, `UPDATE dns_settings SET report=(SELECT jsonb_build_object('domain','example.com','records',jsonb_agg(jsonb_build_object('name','region-'||i)),'candidates',jsonb_agg(jsonb_build_object('ip','198.18.0.'||i))) FROM generate_series(1,125) i) WHERE id`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.db.Exec(ctx, `INSERT INTO dns_sync_runs(started_at,report) SELECT now(),report FROM dns_settings CROSS JOIN generate_series(1,55) i`)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"records", "candidates", "history"} {
		handler := func(w http.ResponseWriter, r *http.Request) { r.SetPathValue("kind", kind); a.listDNSItems(w, r) }
		w := pageRequest(t, handler, "/dns/list?page=2", 200)
		var rows []json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		want := 50
		total := "125"
		if kind == "history" {
			want = 5
			total = "55"
		}
		if len(rows) != want || w.Header().Get("X-Total-Count") != total {
			t.Fatalf("%s: %d %v", kind, len(rows), w.Header())
		}
		if kind == "history" && strings.Contains(w.Body.String(), "candidates") {
			t.Fatal("history includes complete reports")
		}
	}
	w := pageRequest(t, a.getDNS, "/dns?summary=1", 200)
	var status struct {
		Report  map[string]any `json:"report"`
		History []any          `json:"history"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Report["candidate_count"] != float64(125) || status.Report["records"] != nil || status.Report["candidates"] != nil || len(status.History) != 0 {
		t.Fatal("summary contains full lists")
	}
}

func TestCFDetailPagination(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	var id string
	if err := a.db.QueryRow(ctx, `INSERT INTO cf_targets(name,url) VALUES('detail','https://detail.example') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(ctx, `INSERT INTO cf_results(target_id,ip,available,delay_ms,http_status,checked_at) SELECT $1,('198.18.0.'||i)::inet,true,20,200,now() FROM generate_series(1,45) i`, id); err != nil {
		t.Fatal(err)
	}
	handler := func(w http.ResponseWriter, r *http.Request) { r.SetPathValue("id", id); a.getCFTarget(w, r) }
	for page, want := range []int{20, 20, 5} {
		w := pageRequest(t, handler, fmt.Sprintf("/cf-targets/%s?page=%d", id, page+1), 200)
		var detail struct {
			Items []any `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if len(detail.Items) != want || w.Header().Get("X-Total-Count") != "45" {
			t.Fatalf("%d %v", len(detail.Items), w.Header())
		}
	}
}
