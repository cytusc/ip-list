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

func TestConnectivityUsesOnlyGRPC(t *testing.T) {
	for _, scheme := range []string{"vless", "trojan"} {
		for _, transport := range []string{"grpc", "ws", "tcp", "xhttp", "httpupgrade"} {
			raw := scheme + "://" + testUUID + "@sample.invalid:443?type=" + transport
			want := ""
			if transport == "grpc" {
				want = connectivityProtocol
			}
			if got := nodeConnectivityProtocol(raw); got != want {
				t.Fatalf("%s: %q", raw, got)
			}
			if want == "" {
				_, status, _ := (&app{}).launchNodeScan(context.Background(), "", scanInput{}, raw, "", nil, nil, nil, false)
				if status != 422 {
					t.Fatalf("non-gRPC scan accepted: %s", raw)
				}
			}
		}
	}
}

func TestSharedGRPCConnectivity(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("requires TEST_DATABASE_URL")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(config.ConnConfig.Database, "_test") {
		t.Fatal("requires test database")
	}
	admin, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	fixture := fmt.Sprintf("shared_grpc_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+fixture); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+fixture+" CASCADE")
	config.ConnConfig.RuntimeParams["search_path"] = fixture + ",public"
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(schema)
	raw := "vless://" + testUUID + "@sample.invalid:443?security=tls&type=grpc"
	exec(`INSERT INTO nodes(name,address,port,share_uri) VALUES('sample','sample.invalid',443,$1),
 ('ws','sample.invalid',443,$2),('trojan','sample.invalid',443,'trojan://password@sample.invalid:443?type=grpc'),
 ('disabled','sample.invalid',443,$2)`, raw, strings.Replace(raw, "type=grpc", "type=ws", 1))
	exec("UPDATE nodes SET enabled=false WHERE name='disabled'")
	exec(`INSERT INTO networks(name,cidr) VALUES('fixture','198.18.93.0/30')`)
	// Simulate pre-upgrade results and duplicate automatic queues/schedules.
	exec("DELETE FROM app_migrations WHERE name='shared_grpc_connectivity_v1'")
	exec(`INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at,config_hash,certificate)
 VALUES('vless/grpc','198.18.93.1',true,42,now(),md5($1),'{"status":"passed","server_name":"sample.invalid"}'::jsonb)`, raw)
	exec("INSERT INTO users(username,password_hash) VALUES('fixture','fixture')")
	exec(`INSERT INTO network_auto_scans(network_id,cidr,protocol,user_id)
 SELECT w.id,w.cidr,p.protocol,u.id FROM networks w CROSS JOIN (VALUES('vless/grpc'),('vless/ws')) p(protocol) CROSS JOIN users u`)
	exec(`INSERT INTO node_scan_schedules(protocol,job_id,job) VALUES('vless/grpc','grpc-job','{"protocol":"vless/grpc"}'),('vless/ws','ws-job','{}')`)
	exec(schema)
	exec(schema) // Initialization remains idempotent.
	count := func(query string, want int, args ...any) {
		t.Helper()
		var got int
		if err := db.QueryRow(ctx, query, args...).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got=%d want=%d err=%v", query, got, want, err)
		}
	}
	count("SELECT count(*) FROM network_auto_scans WHERE protocol='grpc'", 1)
	count("SELECT count(*) FROM network_auto_scans WHERE protocol<>'grpc'", 0)
	count("SELECT count(*) FROM node_scan_schedules WHERE enabled AND protocol='grpc'", 1)
	count("SELECT count(*) FROM node_scan_schedules WHERE enabled AND protocol<>'grpc'", 0)
	count("SELECT count(*) FROM shared_node_ip_pool WHERE available", 3)
	a := &app{db: db}
	input, sample, _, _, err := a.automaticScanSample(ctx, connectivityProtocol)
	if err != nil || nodeConnectivityProtocol(sample) != connectivityProtocol || input.NodeID == "" {
		t.Fatalf("sample: %s %v", sample, err)
	}
	recorder := httptest.NewRecorder()
	a.generateIPLinks(recorder, httptest.NewRequest("GET", "/available-ips/links?ip=198.18.93.1", nil))
	var links []generatedLink
	if recorder.Code != 200 || json.Unmarshal(recorder.Body.Bytes(), &links) != nil || len(links) != 3 {
		t.Fatalf("shared links: %d %s", recorder.Code, recorder.Body.String())
	}
	for _, protocol := range []string{"vless/grpc", "vless/ws", "trojan/grpc"} {
		recorder = httptest.NewRecorder()
		a.availableIPs(recorder, httptest.NewRequest("GET", "/available-ips?protocol="+protocol, nil))
		var ips []availableIP
		if recorder.Code != 200 || json.Unmarshal(recorder.Body.Bytes(), &ips) != nil || len(ips) != 1 {
			t.Fatalf("shared list %s: %d %s", protocol, recorder.Code, recorder.Body.String())
		}
		count("SELECT count(*) FROM shared_node_ip_pool WHERE available AND protocol=$1", 1, protocol)
		candidates, _, err := a.dnsCandidates(ctx, dnsConfig{Protocol: protocol, Window: 5})
		if err != nil || len(candidates) != 1 || candidates[0].IP != "198.18.93.1" {
			t.Fatalf("shared DNS %s: %v %v", protocol, candidates, err)
		}
	}
	// A newer gRPC failure removes the IP for every protocol.
	exec("UPDATE node_ip_pool SET available=false,checked_at=now() WHERE protocol='grpc'")
	count("SELECT count(*) FROM shared_node_ip_pool WHERE available", 0)
	exec("UPDATE node_ip_pool SET available=true WHERE protocol='grpc'")
	// Legacy certificate errors must not override fresh gRPC evidence.
	exec(`UPDATE node_ip_pool SET certificate='{"status":"invalid","server_name":"sample.invalid"}' WHERE protocol='vless/grpc'`)
	count("SELECT count(*) FROM shared_node_ip_pool WHERE available", 3)
	// Editing the sample invalidates inherited results too.
	exec("UPDATE nodes SET share_uri=share_uri||'&sni=changed.invalid' WHERE name='sample'")
	count("SELECT count(*) FROM shared_node_ip_pool WHERE available", 0)
}
