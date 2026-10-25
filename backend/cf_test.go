package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func TestCFScanTargetsBeyondNodeLimit(t *testing.T) {
	for _, cidrs := range [][]string{
		{"192.0.0.0/19"},
		{"192.0.0.0/20", "192.0.16.0/20", "192.0.0.0/24"},
		{"2001:db8::/115", "2001:db8::/128"},
	} {
		total, targets, err := cfScanTargets(cidrs)
		if err != nil || total != 8192 {
			t.Fatalf("%v: total=%d err=%v", cidrs, total, err)
		}
		seen := map[netip.Addr]bool{}
		for ip := range targets {
			if seen[ip] {
				t.Fatalf("duplicate address %s", ip)
			}
			seen[ip] = true
		}
		if len(seen) != total {
			t.Fatalf("visited %d of %d addresses", len(seen), total)
		}
		if _, err := expandPrefixes(cidrs); err == nil {
			t.Fatal("node scan must retain its 4096 IP limit")
		}
	}
}

func TestCFScanRoundHasNoTimeLimit(t *testing.T) {
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	ctx, cancel := scanRoundContext(parent, true)
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("CF scan must not have a round deadline")
	}
	stop()
	if ctx.Err() != context.Canceled {
		t.Fatal("CF scan must still stop when its parent is cancelled")
	}
	nodeCtx, nodeCancel := scanRoundContext(context.Background(), false)
	defer nodeCancel()
	if _, ok := nodeCtx.Deadline(); !ok {
		t.Fatal("node scan must retain its round deadline")
	}
}

func TestCFScanTargetsLazyAndBoundaries(t *testing.T) {
	total, targets, err := cfScanTargets([]string{"0.0.0.0/0"})
	if err != nil || int64(total) != 1<<32 {
		t.Fatalf("large range: total=%d err=%v", total, err)
	}
	visited := 0
	for range targets {
		visited++
		if visited == 3 {
			break // Must stop without expanding or allocating the entire /0.
		}
	}
	total, targets, err = cfScanTargets([]string{"255.255.255.255/32", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff/128"})
	if err != nil || total != 2 || len(slices.Collect(targets)) != 2 {
		t.Fatalf("address boundary: total=%d err=%v", total, err)
	}
	for _, cidrs := range [][]string{nil, {"invalid"}, {"192.0.2.1/24"}, {"::ffff:192.0.2.0/120"}, {"::/0"}} {
		if _, _, err := cfScanTargets(cidrs); err == nil {
			t.Fatalf("accepted invalid or unrepresentable range %v", cidrs)
		}
	}
}

func TestCFProbePinnedTLS(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Host, "example.com:") || r.TLS.ServerName != "example.com" {
			t.Errorf("lost original Host/SNI: %s %s", r.Host, r.TLS.ServerName)
		}
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "https://unreachable.invalid/", 302)
		case "/blocked":
			w.WriteHeader(403)
		case "/slow":
			<-r.Context().Done()
		default:
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	config := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	ip := netip.MustParseAddr("127.0.0.1")
	for _, tc := range []struct {
		path string
		code int
		fail bool
	}{{"/", 204, false}, {"/redirect", 302, false}, {"/blocked", 403, true}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		code, err := probeCF(ctx, ip, "https://example.com:"+port+tc.path, config)
		cancel()
		if code != tc.code || (err != nil) != tc.fail {
			t.Fatalf("%s: code=%d err=%v", tc.path, code, err)
		}
	}
	if hits.Load() != 3 {
		t.Fatal("redirect must not be followed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := probeCF(ctx, ip, "https://example.com:"+port+"/slow", config); err == nil {
		t.Fatal("timeout ignored")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if _, err := probeCF(ctx2, ip, "https://wrong.invalid:"+port, config); err == nil {
		t.Fatal("certificate mismatch accepted")
	}
}

func TestCFPersistence(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("requires test database")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !strings.HasSuffix(db.Config().ConnConfig.Database, "_test") {
		t.Fatal("requires _test database")
	}
	if _, err = db.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	var id string
	if err = db.QueryRow(ctx, "INSERT INTO cf_targets(name,url) VALUES('test',$1) RETURNING id::text", fmt.Sprintf("https://%d.example", time.Now().UnixNano())).Scan(&id); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.Exec(ctx, "DELETE FROM cf_targets WHERE id=$1", id) }()
	defer func() { _, _ = db.Exec(ctx, "DELETE FROM cf_ip_pool WHERE ip IN ('192.0.2.1','192.0.2.2')") }()
	a := &app{db: db}
	now := time.Now().UTC()
	j := scanJob{TargetID: id, FinishedAt: &now, Results: []scanResult{{IP: "192.0.2.1", Status: "success", Delay: 20, HTTPStatus: 200}, {IP: "192.0.2.2", Status: "failed"}}}
	save := func() {
		t.Helper()
		if err := a.saveScanResults(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	count := func(want int) {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM cf_ip_pool WHERE ip IN ('192.0.2.1','192.0.2.2') AND available").Scan(&n); err != nil || n != want {
			t.Fatalf("available=%d want=%d err=%v", n, want, err)
		}
	}
	save()
	count(1)
	// A newly configured domain must immediately see the shared IP without a scan.
	var secondID string
	if err = db.QueryRow(ctx, "INSERT INTO cf_targets(name,url) VALUES('second',$1) RETURNING id::text", fmt.Sprintf("https://second-%d.example", time.Now().UnixNano())).Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.Exec(ctx, "DELETE FROM cf_targets WHERE id=$1", secondID) }()
	options, err := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	a.redis = redis.NewClient(options)
	defer a.redis.Close()
	readShared := func(targetID string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/cf-targets/"+targetID, nil)
		req.SetPathValue("id", targetID)
		req = req.WithContext(context.WithValue(ctx, userContextKey{}, user{ID: testUUID}))
		response := httptest.NewRecorder()
		a.getCFTarget(response, req)
		var body struct {
			Items []struct {
				IP string `json:"ip"`
			} `json:"items"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
			t.Fatalf("shared pool response: %d %s", response.Code, response.Body.String())
		}
		for _, item := range body.Items {
			if item.IP == "192.0.2.1" {
				return
			}
		}
		t.Fatal("new domain did not inherit shared IP")
	}
	readShared(id)
	readShared(secondID)
	// Deleting the source domain must not remove the reusable IP pool.
	if _, err = db.Exec(ctx, "DELETE FROM cf_targets WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	readShared(secondID)
	// A newer scan through another domain updates the same pool.
	j.TargetID = secondID
	later := now.Add(time.Second)
	j.FinishedAt = &later
	j.Results = j.Results[:1]
	j.Results[0].Status = "failed"
	save()
	count(0)
	j.FinishedAt = &now
	j.Results[0].Status = "success"
	save()
	count(0)
	j.Results = nil
	save()
	count(0)
	// Upgrade old per-domain results, keeping newer shared failures intact.
	if _, err = db.Exec(ctx, `INSERT INTO cf_results(target_id,ip,available,delay_ms,http_status,checked_at)
 VALUES($1,'192.0.2.1',true,0,200,$2),($1,'192.0.2.2',true,0,200,$3)`, secondID, later.Add(time.Second), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = db.Exec(ctx, schema); err != nil {
			t.Fatal(err)
		}
		count(1)
	}
}
