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

func TestAvailableIPLifecycle(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("设置 TEST_DATABASE_URL 可验证可用 IP 持久化")
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
	_, _ = db.Exec(ctx, "DELETE FROM node_ip_pool WHERE ip IN ('192.0.2.91','2001:db8::91')")
	var ids, networkIDs []string
	defer func() {
		_, _ = db.Exec(ctx, "DELETE FROM node_ip_pool WHERE ip IN ('192.0.2.91','2001:db8::91')")
		for _, id := range networkIDs {
			_, _ = db.Exec(ctx, "DELETE FROM networks WHERE id=$1", id)
		}
		for _, id := range ids {
			_, _ = db.Exec(ctx, "DELETE FROM nodes WHERE id=$1", id)
		}
	}()
	makeJob := func(protocol string) scanJob {
		t.Helper()
		raw := fmt.Sprintf("vless://%s@original.invalid:443?security=tls&type=%s", testUUID, protocol)
		var id string
		if err := db.QueryRow(ctx, "INSERT INTO nodes(name,address,port,share_uri) VALUES($1,'original.invalid',443,$2) RETURNING id::text", fmt.Sprintf("available-%s-%d", protocol, time.Now().UnixNano()), raw).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		fingerprint := md5.Sum([]byte(raw))
		now := time.Now().UTC()
		return scanJob{Input: scanInput{NodeID: id}, Protocol: "vless/" + protocol, ConfigHash: hex.EncodeToString(fingerprint[:]), FinishedAt: &now}
	}
	save := func(j scanJob) {
		t.Helper()
		for i := range j.Results {
			if j.Results[i].Status == "success" {
				j.Results[i].Certificate = &certificateCheck{Status: "passed", ServerName: "original.invalid", Attempts: 3}
			}
		}
		if err := a.saveIPResults(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	list := func() []availableIP {
		t.Helper()
		w := httptest.NewRecorder()
		a.availableIPs(w, httptest.NewRequest("GET", "/available-ips", nil))
		if w.Code != 200 {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		var items []availableIP
		if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		// Other integration fixtures may exist; examine only this test's two IPs.
		own := []availableIP{}
		for _, item := range items {
			if item.IP == "192.0.2.91" || item.IP == "2001:db8::91" {
				own = append(own, item)
			}
		}
		return own
	}
	first, second := makeJob("httpupgrade"), makeJob("grpc")
	first.Results = []scanResult{{IP: "192.0.2.91", Status: "success", Delay: 30}, {IP: "2001:db8::91", Status: "failed"}}
	second.Results = []scanResult{{IP: "192.0.2.91", Status: "success", Delay: 50}}
	save(first)
	save(second)
	if items := list(); len(items) != 1 || len(items[0].Checks) != 2 {
		t.Fatalf("cross-node merge: %+v", items)
	}

	links := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		a.generateIPLinks(w, httptest.NewRequest("GET", "/available-ips/links?ip=192.0.2.91", nil))
		if want == 0 {
			if w.Code != 409 {
				t.Fatalf("stale link generated: %d", w.Code)
			}
			return
		}
		var result []generatedLink
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result) != want {
			t.Fatalf("links response: %d", w.Code)
		}
	}
	links(2)
	// Prefer more specific metadata; an empty /32 falls back to the annotated /24.
	for _, cidr := range []string{"192.0.2.0/24", "192.0.2.91/32"} {
		var networkID string
		if err = db.QueryRow(ctx, "INSERT INTO networks(name,cidr,asn,provider) VALUES('metadata',$1::cidr,$2,$3) RETURNING id::text", cidr, map[bool]string{true: "AS13335", false: ""}[cidr == "192.0.2.0/24"], map[bool]string{true: "Cloudflare", false: ""}[cidr == "192.0.2.0/24"]).Scan(&networkID); err != nil {
			t.Fatal(err)
		}
		networkIDs = append(networkIDs, networkID)
	}
	if got := list()[0]; got.ASN != "AS13335" || got.Provider != "Cloudflare" {
		t.Fatalf("metadata: %+v", got)
	}
	if _, err = db.Exec(ctx, "UPDATE networks SET asn='AS64500',provider='Specific' WHERE cidr='192.0.2.91/32'"); err != nil {
		t.Fatal(err)
	}
	if got := list()[0]; got.ASN != "AS64500" || got.Provider != "Specific" {
		t.Fatalf("specific metadata: %+v", got)
	}
	// Database data remains available without Redis or an in-memory scan job.
	later := first.FinishedAt.Add(time.Second)
	first.FinishedAt = &later
	first.Results[0].Status = "failed"
	save(first)
	if items := list(); len(items) != 1 || len(items[0].Checks) != 1 || items[0].Checks[0].Protocol != "vless/grpc" {
		t.Fatalf("failure did not remove only its own protocol: %+v", items)
	}
	links(1)
	// An older result may not resurrect a protocol after a newer failure.
	older := later.Add(-2 * time.Second)
	first.FinishedAt = &older
	first.Results[0].Status = "success"
	save(first)
	if items := list(); len(items) != 1 || len(items[0].Checks) != 1 {
		t.Fatalf("older scan overwrote latest: %+v", items)
	}
	second.Results = nil
	save(second)
	if len(list()) != 1 {
		t.Fatal("empty canceled round removed existing results")
	}
	// Editing the last matching configuration invalidates its old shared results.
	if _, err = db.Exec(ctx, "UPDATE nodes SET share_uri=share_uri || '&sni=changed.example' WHERE id=$1", second.Input.NodeID); err != nil {
		t.Fatal(err)
	}
	second.Results = []scanResult{{IP: "192.0.2.91", Status: "success", Delay: 20}}
	save(second)
	if len(list()) != 0 {
		t.Fatal("edited configuration reused stale results")
	}
	links(0)
	// A later success can restore the first node, and disabled/deleted nodes disappear.
	restored := later.Add(time.Second)
	first.FinishedAt = &restored
	save(first)
	if len(list()) != 1 {
		t.Fatal("recovery was not saved")
	}
	if _, err = db.Exec(ctx, "UPDATE nodes SET enabled=false WHERE id=$1", first.Input.NodeID); err != nil {
		t.Fatal(err)
	}
	if len(list()) != 0 {
		t.Fatal("disabled configuration still advertised as available")
	}
	links(0)
	if _, err = db.Exec(ctx, "DELETE FROM nodes WHERE id=$1", first.Input.NodeID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(ctx, "SELECT count(*) FROM ip_results WHERE node_id=$1", first.Input.NodeID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("node deletion did not cascade: %d %v", count, err)
	}
}

// Synthetic pool fixtures still need evidence tied to an enabled configuration.
func attachPoolConfig(t *testing.T, db *pgxpool.Pool, protocols ...string) {
	t.Helper()
	ctx := context.Background()
	raw := fmt.Sprintf("vless://%s@fixture.invalid:443?security=tls&type=ws#fixture-%d", testUUID, time.Now().UnixNano())
	var id string
	if err := db.QueryRow(ctx, "INSERT INTO nodes(name,address,port,share_uri) VALUES($1,'fixture.invalid',443,$2) RETURNING id::text", "pool-fixture-"+fmt.Sprint(time.Now().UnixNano()), raw).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, err := pgxpool.New(ctx, db.Config().ConnString())
		if err != nil {
			t.Error(err)
			return
		}
		defer cleanup.Close()
		if _, err = cleanup.Exec(ctx, "DELETE FROM nodes WHERE id=$1", id); err != nil {
			t.Error(err)
		}
	})
	if _, err := db.Exec(ctx, `UPDATE node_ip_pool SET config_hash=md5($1),certificate='{"status":"passed","server_name":"fixture.invalid","attempts":3}'::jsonb WHERE protocol=ANY($2::text[])`, raw, protocols); err != nil {
		t.Fatal(err)
	}
}

func TestPoolRejectsReplacedConfiguration(t *testing.T) {
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
	const ip = "198.18.97.11"
	var id string
	oldRaw := "vless://" + testUUID + "@old.example:443?security=tls&type=ws#replacement-test"
	if err = db.QueryRow(ctx, "INSERT INTO nodes(name,address,port,share_uri) VALUES('replacement-test','old.example',443,$1) RETURNING id::text", oldRaw).Scan(&id); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, "DELETE FROM nodes WHERE id=$1", id)
	defer db.Exec(ctx, "DELETE FROM node_ip_pool WHERE ip=$1::inet", ip)
	hash := md5.Sum([]byte(oldRaw))
	now := time.Now().UTC()
	a := &app{db: db}
	old := scanJob{Protocol: "vless/ws", ConfigHash: hex.EncodeToString(hash[:]), Input: scanInput{NodeID: id}, FinishedAt: &now, Results: []scanResult{{IP: ip, Status: "success", Delay: 10, Certificate: &certificateCheck{Status: "passed", ServerName: "old.example", Attempts: 3}}}}
	save := func(job scanJob) {
		t.Helper()
		if err := a.saveIPResults(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	visible := func(want bool) {
		t.Helper()
		var got bool
		if err := db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM current_node_ip_pool WHERE ip=$1::inet AND available)", ip).Scan(&got); err != nil || got != want {
			t.Fatalf("visible=%v want=%v err=%v", got, want, err)
		}
	}
	save(old)
	visible(true)
	// Upgrade retains results with provenance but excludes orphaned legacy rows.
	if _, err = db.Exec(ctx, "UPDATE node_ip_pool SET config_hash='' WHERE ip=$1::inet", ip); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	visible(true)
	newRaw := strings.Replace(oldRaw, "old.example", "new.example", 1)
	if _, err = db.Exec(ctx, "UPDATE nodes SET share_uri=$1 WHERE id=$2", newRaw, id); err != nil {
		t.Fatal(err)
	}
	visible(false)
	checked, err := a.checkedScanIPs(ctx, []string{ip + "/32"}, old.Protocol)
	if err != nil || len(checked) != 0 {
		t.Fatal("replacement skipped stale addresses", checked, err)
	}
	w := httptest.NewRecorder()
	a.generateIPLinks(w, httptest.NewRequest("GET", "/available-ips/links?ip="+ip, nil))
	if w.Code != 409 {
		t.Fatalf("stale link advertised: %d", w.Code)
	}
	fresh := old
	hash = md5.Sum([]byte(newRaw))
	fresh.ConfigHash = hex.EncodeToString(hash[:])
	later := now.Add(time.Second)
	fresh.FinishedAt = &later
	save(fresh)
	visible(true)
	// A late old worker cannot replace fresh results even with a newer timestamp.
	latest := later.Add(time.Second)
	old.FinishedAt = &latest
	old.Results = []scanResult{{IP: ip, Status: "failed"}}
	save(old)
	visible(true)
}
