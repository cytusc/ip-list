package main

import (
	"context"
	"encoding/json"
	"net/netip"
	"testing"
	"time"
)

func TestAvailableIPSpeedSurvivesOutageAndRetries(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	exec := func(sql string) {
		t.Helper()
		if _, err := a.db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO nodes(name,address,port,share_uri) VALUES('speed-fixture','sample.invalid',443,'vless://test@sample.invalid:443?type=grpc&security=tls');
 INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at,config_hash,certificate)
 SELECT 'grpc',ip::inet,true,20,now(),md5('vless://test@sample.invalid:443?type=grpc&security=tls'),'{"status":"passed"}'::jsonb
 FROM unnest(ARRAY['192.0.2.1','192.0.2.2','192.0.2.3','192.0.2.4','192.0.2.5']) ip;
 INSERT INTO available_ip_speed_tests(ip,duration_ms,speed_mib_s,checked_at,success) VALUES
 ('192.0.2.1',1000,20,now(),true),
 ('192.0.2.2',1000,20,now()-interval '5 hours',true),
 ('192.0.2.3',0,0,now(),false),
 ('192.0.2.4',0,0,now()-interval '6 minutes',false);`)
	// Fresh health evidence is absent after an outage; an empty pool must not erase history.
	a.runAvailableIPSpeedTests(ctx)
	var count int
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM available_ip_speed_tests`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("history lost while pool unavailable: count=%d err=%v", count, err)
	}
	verifyPoolFixture(t, a.db)
	// The upgrade backfills timestamps for successful results saved by older versions.
	exec(`ALTER TABLE available_ip_speed_tests DROP COLUMN last_success_at`)
	exec(schema)
	var lastSuccess time.Time
	if err := a.db.QueryRow(ctx, `SELECT last_success_at FROM available_ip_speed_tests WHERE ip='192.0.2.1'`).Scan(&lastSuccess); err != nil {
		t.Fatal(err)
	}
	var recentFailure time.Time
	if err := a.db.QueryRow(ctx, `SELECT checked_at FROM available_ip_speed_tests WHERE ip='192.0.2.3'`).Scan(&recentFailure); err != nil {
		t.Fatal(err)
	}
	// No HK2-gRPC template: selected due IPs receive failure states without network traffic.
	a.runAvailableIPSpeedTests(ctx)
	var success bool
	var checked time.Time
	if err := a.db.QueryRow(ctx, `SELECT success FROM available_ip_speed_tests WHERE ip='192.0.2.1'`).Scan(&success); err != nil || !success {
		t.Fatalf("fresh success was retested: %v %v", success, err)
	}
	for _, ip := range []string{"192.0.2.2", "192.0.2.4", "192.0.2.5"} {
		if err := a.db.QueryRow(ctx, `SELECT success,checked_at FROM available_ip_speed_tests WHERE ip=$1::inet`, ip).Scan(&success, &checked); err != nil || success || time.Since(checked) > time.Minute {
			t.Fatalf("due/recovered IP not retried: ip=%s success=%v checked=%v err=%v", ip, success, checked, err)
		}
	}
	if err := a.db.QueryRow(ctx, `SELECT checked_at FROM available_ip_speed_tests WHERE ip='192.0.2.3'`).Scan(&checked); err != nil || !checked.Equal(recentFailure) {
		t.Fatalf("recent failure retried too soon: %v %v", checked, err)
	}
	// A failed retry keeps successful metrics but cannot qualify for new DNS publication.
	ip := netip.MustParseAddr("192.0.2.1")
	if err := a.saveAvailableIPSpeedResult(ctx, ip, availableIPSpeedResult{}, false); err != nil {
		t.Fatal(err)
	}
	var duration int64
	var speed float64
	if err := a.db.QueryRow(ctx, `SELECT duration_ms,speed_mib_s,success FROM available_ip_speed_tests WHERE ip=$1::inet`, ip.String()).Scan(&duration, &speed, &success); err != nil || duration != 1000 || speed != 20 || success {
		t.Fatalf("failure erased successful metrics: %d %v %v %v", duration, speed, success, err)
	}
	items := []dnsCandidate{{IP: ip.String(), Country: "HK", State: "healthy"}}
	if err := a.filterDNSSlowIPs(ctx, items); err != nil || items[0].State != "pending" {
		t.Fatalf("historical speed qualified: %+v %v", items, err)
	}
	response := pageRequest(t, a.availableIPs, "/available-ips", 200)
	var listed []availableIP
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range listed {
		if item.IP == ip.String() {
			found = true
			if item.SpeedTest == nil || item.SpeedTest.Success || item.SpeedPassed || item.SpeedTest.SpeedMiBs != 20 || item.SpeedTest.LastSuccessAt == nil || !item.SpeedTest.LastSuccessAt.Equal(lastSuccess) {
				t.Fatalf("API lost historical speed or marked it current: %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("recovered IP missing from API")
	}
	var retained time.Time
	if err := a.db.QueryRow(ctx, `SELECT last_success_at FROM available_ip_speed_tests WHERE ip=$1::inet`, ip.String()).Scan(&retained); err != nil || !retained.Equal(lastSuccess) {
		t.Fatalf("failure changed success timestamp: %v %v", retained, err)
	}
	if err := a.saveAvailableIPSpeedResult(ctx, ip, availableIPSpeedResult{Duration: 2000, SpeedMiBs: 30}, true); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(ctx, `SELECT duration_ms,speed_mib_s,success,last_success_at FROM available_ip_speed_tests WHERE ip=$1::inet`, ip.String()).Scan(&duration, &speed, &success, &retained); err != nil || duration != 2000 || speed != 30 || !success || !retained.After(lastSuccess) {
		t.Fatalf("recovery did not replace metrics: %d %v %v %v %v", duration, speed, success, retained, err)
	}
}
