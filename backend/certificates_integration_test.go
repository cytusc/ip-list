package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCertificateQuarantineLifecycle(t *testing.T) {
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
	a := &app{db: db}
	ip := "198.18.96.121"
	ids := []string{}
	defer func() {
		db.Exec(ctx, "DELETE FROM node_ip_pool WHERE ip=$1::inet", ip)
		for _, id := range ids {
			db.Exec(ctx, "DELETE FROM nodes WHERE id=$1", id)
		}
	}()
	makeJob := func(transport, sni string) scanJob {
		raw := fmt.Sprintf("vless://%s@ingress.invalid:443?security=tls&type=%s&sni=%s", testUUID, transport, sni)
		var id string
		if err := db.QueryRow(ctx, "INSERT INTO nodes(name,address,port,share_uri) VALUES($1,'ingress.invalid',443,$2) RETURNING id::text", fmt.Sprintf("cert-%s-%d", transport, time.Now().UnixNano()), raw).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		hash := md5.Sum([]byte(raw))
		now := time.Now().UTC()
		return scanJob{Input: scanInput{NodeID: id}, Protocol: "vless/" + transport, ConfigHash: hex.EncodeToString(hash[:]), FinishedAt: &now, Results: []scanResult{{IP: ip, Status: "success", Delay: 10}}}
	}
	first, second, other := makeJob("ws", "entry.example"), makeJob("grpc", "entry.example"), makeJob("xhttp", "other.example")
	save := func(job *scanJob) {
		t.Helper()
		now := time.Now().UTC()
		job.FinishedAt = &now
		if err := a.saveIPResults(ctx, *job); err != nil {
			t.Fatal(err)
		}
	}
	count := func(want int) {
		t.Helper()
		var got int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM current_node_ip_pool WHERE ip=$1::inet AND available", ip).Scan(&got); err != nil || got != want {
			t.Fatalf("eligible=%d want=%d err=%v", got, want, err)
		}
	}
	issues := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		a.certificateIssues(w, httptest.NewRequest("GET", "/certificate-issues", nil))
		var rows []json.RawMessage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rows) != nil || len(rows) != want {
			t.Fatalf("issues=%d %s", w.Code, w.Body.String())
		}
	}
	// A historical success without certificate evidence is deliberately not eligible.
	save(&first)
	count(0)
	checked, err := a.checkedScanIPs(ctx, []string{ip + "/32"}, first.Protocol)
	if err != nil || len(checked) != 0 {
		t.Fatalf("unchecked legacy result skipped revalidation: %v %v", checked, err)
	}
	first.Results[0].Certificate = &certificateCheck{Status: "passed", ServerName: "entry.example", Attempts: 3}
	save(&first)
	count(1)
	second.Results[0].Status = "failed"
	second.Results[0].Certificate = &certificateCheck{Status: "invalid", ServerName: "entry.example", Subject: "CN=www.paypal.com", Error: "证书域名不匹配", Attempts: 2}
	save(&second)
	count(0)
	issues(1)
	// Both the invalid result and the cross-protocol quarantined success
	// remain checked, so incremental queue processing can advance.
	for _, protocol := range []string{first.Protocol, second.Protocol} {
		checked, err := a.checkedScanIPs(ctx, []string{ip + "/32"}, protocol)
		if err != nil || len(checked) != 1 || checked[0] != ip {
			t.Fatalf("quarantined result not counted as checked: protocol=%s checked=%v err=%v", protocol, checked, err)
		}
	}
	// A different SNI has independent certificate evidence.
	other.Results[0].Certificate = &certificateCheck{Status: "passed", ServerName: "other.example", Attempts: 3}
	save(&other)
	count(1)
	// An ordinary timeout must not erase an unresolved certificate failure.
	second.Results[0].Certificate = &certificateCheck{Status: "error", ServerName: "entry.example", Error: "TLS 连接超时"}
	save(&second)
	count(1)
	issues(1)
	// A later complete validation resolves quarantine for the same SNI.
	stale := second
	second.Results[0].Status = "success"
	second.Results[0].Certificate = &certificateCheck{Status: "passed", ServerName: "entry.example", Attempts: 3}
	save(&second)
	count(3)
	issues(0)
	// Older results cannot re-quarantine a newly verified IP.
	stale.Results = []scanResult{{IP: ip, Status: "failed", Certificate: &certificateCheck{Status: "invalid", ServerName: "entry.example"}}}
	if err := a.saveIPResults(ctx, stale); err != nil {
		t.Fatal(err)
	}
	count(3)
	issues(0)
	// Re-running migrations must retain validated evidence.
	if _, err := db.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	count(3)
}

func TestCertificateIssuesPagination(t *testing.T) {
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
	const protocol = "cert-pagination-test"
	raw := fmt.Sprintf("vless://%s@pagination.invalid:443#%d", testUUID, time.Now().UnixNano())
	var id string
	if err = db.QueryRow(ctx, "INSERT INTO nodes(name,address,port,share_uri) VALUES($1,'pagination.invalid',443,$2) RETURNING id::text", fmt.Sprintf("cert-page-%d", time.Now().UnixNano()), raw).Scan(&id); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, "DELETE FROM nodes WHERE id=$1", id)
	defer db.Exec(ctx, "DELETE FROM node_ip_pool WHERE protocol=$1", protocol)
	_, err = db.Exec(ctx, `INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at,config_hash,certificate)
	 SELECT $1,('198.19.96.'||i)::inet,false,0,now(),CASE WHEN i=107 THEN 'obsolete' ELSE md5($2) END,
	 CASE WHEN i=106 THEN '{"status":"passed"}'::jsonb ELSE '{"status":"invalid","server_name":"pagination.invalid"}'::jsonb END
	 FROM generate_series(1,107) i`, protocol, raw)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{db: db}
	seen := map[string]bool{}
	for _, tc := range []struct{ page, length, actual string }{{"1", "50", "1"}, {"2", "50", "2"}, {"3", "5", "3"}, {"999999", "5", "3"}} {
		w := httptest.NewRecorder()
		a.certificateIssues(w, httptest.NewRequest("GET", "/certificate-issues?page="+tc.page, nil))
		var items []struct {
			IP string `json:"ip"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &items) != nil || fmt.Sprint(len(items)) != tc.length || w.Header().Get("X-Page") != tc.actual || w.Header().Get("X-Total-Count") != "105" || w.Header().Get("X-Page-Size") != "50" {
			t.Fatalf("page %s: status=%d headers=%v body=%s", tc.page, w.Code, w.Header(), w.Body.String())
		}
		if tc.page == "999999" {
			continue
		}
		for _, item := range items {
			if seen[item.IP] {
				t.Fatalf("duplicate across tied-timestamp pages: %s", item.IP)
			}
			seen[item.IP] = true
		}
	}
	if len(seen) != 105 {
		t.Fatalf("pagination omitted records: %d", len(seen))
	}
	for _, page := range []string{"0", "-1", "abc", "9999999999999999999999999"} {
		w := httptest.NewRecorder()
		a.certificateIssues(w, httptest.NewRequest("GET", "/certificate-issues?page="+page, nil))
		if w.Code != 422 {
			t.Fatalf("invalid page %s accepted: %d", page, w.Code)
		}
	}
	if _, err = db.Exec(ctx, "UPDATE nodes SET enabled=false WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.certificateIssues(w, httptest.NewRequest("GET", "/certificate-issues?page=999", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" || w.Header().Get("X-Total-Count") != "0" || w.Header().Get("X-Page") != "1" {
		t.Fatalf("empty/disabled nodes: %d %s %v", w.Code, w.Body.String(), w.Header())
	}
}

func TestCertificateLegacyRevalidation(t *testing.T) {
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
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	port := strings.TrimPrefix(server.URL, "https://127.0.0.1:")
	raw := fmt.Sprintf("vless://%s@legacy.invalid:%s?security=tls&type=grpc#upgrade-%d", testUUID, port, time.Now().UnixNano())
	var id string
	if err = db.QueryRow(ctx, "INSERT INTO nodes(name,address,port,share_uri) VALUES($1,'legacy.invalid',1,$2) RETURNING id::text", fmt.Sprint(time.Now().UnixNano()), raw).Scan(&id); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, "DELETE FROM nodes WHERE id=$1", id)
	const protocol = connectivityProtocol
	defer db.Exec(ctx, "DELETE FROM node_ip_pool WHERE protocol=$1", protocol)
	defer db.Exec(ctx, "DELETE FROM ip_blacklist WHERE ip='127.0.0.3'")
	_, err = db.Exec(ctx, `INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at,config_hash,certificate) VALUES
 ($1,'127.0.0.1',true,1,now(),md5($2),'{}'),
 ($1,'127.0.0.3',true,1,now(),md5($2),'{}'),
 ($1,'127.0.0.4',true,1,now(),md5($2),'{"status":"passed","server_name":"legacy.invalid"}');
 `, protocol, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, "INSERT INTO ip_blacklist(ip) VALUES('127.0.0.3')"); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db}
	deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	a.revalidateLegacyCertificates(deadline)
	var available bool
	var status string
	if err = db.QueryRow(ctx, "SELECT available,certificate->>'status' FROM node_ip_pool WHERE protocol=$1 AND ip='127.0.0.1'", protocol).Scan(&available, &status); err != nil || available || status != "invalid" {
		t.Fatalf("legacy validation: available=%v status=%s err=%v", available, status, err)
	}
	for _, ip := range []string{"127.0.0.3", "127.0.0.4"} {
		var unchanged bool
		if err = db.QueryRow(ctx, "SELECT available FROM node_ip_pool WHERE protocol=$1 AND ip=$2::inet", protocol, ip).Scan(&unchanged); err != nil || !unchanged {
			t.Fatalf("rechecked blacklisted/already checked IP %s: %v", ip, err)
		}
	}
}
