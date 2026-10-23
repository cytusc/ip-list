package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

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
		if err := db.QueryRow(ctx, "SELECT count(*) FROM cf_results WHERE target_id=$1 AND available", id).Scan(&n); err != nil || n != want {
			t.Fatalf("available=%d want=%d err=%v", n, want, err)
		}
	}
	save()
	count(1)
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
}
