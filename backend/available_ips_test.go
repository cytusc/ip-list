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
	var ids, networkIDs []string
	defer func() {
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
		return scanJob{Input: scanInput{NodeID: id}, Protocol: protocol, ConfigHash: hex.EncodeToString(fingerprint[:]), FinishedAt: &now}
	}
	save := func(j scanJob) {
		t.Helper()
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
	if items := list(); len(items) != 1 || len(items[0].Checks) != 1 || items[0].Checks[0].Protocol != "grpc" {
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
	// Configuration edits invalidate old evidence and reject a finishing old scan.
	if _, err = db.Exec(ctx, "UPDATE nodes SET share_uri=share_uri || '&sni=changed.example' WHERE id=$1", second.Input.NodeID); err != nil {
		t.Fatal(err)
	}
	second.Results = []scanResult{{IP: "192.0.2.91", Status: "success", Delay: 20}}
	save(second)
	if len(list()) != 0 {
		t.Fatal("edited node exposed old-config results")
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
		t.Fatal("disabled node remained visible")
	}
	if _, err = db.Exec(ctx, "DELETE FROM nodes WHERE id=$1", first.Input.NodeID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(ctx, "SELECT count(*) FROM ip_results WHERE node_id=$1", first.Input.NodeID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("node deletion did not cascade: %d %v", count, err)
	}
}
