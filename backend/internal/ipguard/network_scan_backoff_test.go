package ipguard

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAutomaticFullScanBackoff(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("requires TEST_DATABASE_URL")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(config.ConnConfig.Database, "_test") {
		t.Fatal("requires a test database")
	}
	config.MaxConns = 1 // Temporary tables stay on this session.
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(ctx, `CREATE TEMP TABLE network_auto_scans(network_id uuid,protocol text,cidr cidr,created_at timestamptz,recheck_after timestamptz);
CREATE TEMP TABLE network_scan_backoff(network_id uuid,protocol text,cidr cidr,no_output_streak int,interval_days int,next_scan_at timestamptz,updated_at timestamptz,PRIMARY KEY(network_id,protocol));
CREATE TEMP TABLE node_ip_pool(ip inet,protocol text,available boolean,checked_at timestamptz);
CREATE TEMP VIEW node_ip_evidence AS SELECT * FROM node_ip_pool;
CREATE TEMP TABLE cf_ip_pool(ip inet,available boolean,checked_at timestamptz);`); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db}
	id, cidr := "00000000-0000-0000-0000-000000000001", "198.18.0.0/24"
	cutoff := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
	created := time.Now().UTC().Truncate(time.Microsecond)
	queue := func(protocol string) {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO network_auto_scans VALUES($1,$2,$3,$4,$5)`, id, protocol, cidr, created, cutoff); err != nil {
			t.Fatal(err)
		}
	}
	finish := func(protocol string, timestamp time.Time) {
		t.Helper()
		if err := a.finishAutomaticFullScan(ctx, id, protocol, cidr, timestamp, cutoff); err != nil {
			t.Fatal(err)
		}
	}
	check := func(protocol string, streak, days int) {
		t.Helper()
		var gotStreak, gotDays int
		if err := db.QueryRow(ctx, `SELECT no_output_streak,interval_days FROM network_scan_backoff WHERE network_id=$1 AND protocol=$2`, id, protocol).Scan(&gotStreak, &gotDays); err != nil || gotStreak != streak || gotDays != days {
			t.Fatalf("%s: streak=%d days=%d err=%v", protocol, gotStreak, gotDays, err)
		}
	}
	// Output older than 24h still belongs to this unfinished 48h sweep.
	if _, err = db.Exec(ctx, `INSERT INTO node_ip_pool VALUES('198.18.0.1','grpc',true,$1)`, cutoff.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	queue("grpc")
	finish("grpc", created)
	check("grpc", 0, 1)
	// Node evidence must not count as CF output. Replays must not advance streak.
	for index, days := range []int{1, 1, 3, 5, 7, 15} {
		queue("cf")
		finish("cf", created.Add(-time.Second)) // A replaced queue identity is ignored.
		finish("cf", created)
		finish("cf", created)
		check("cf", index+1, days)
	}
	check("cf", 6, 15)
	if _, err = db.Exec(ctx, `INSERT INTO cf_ip_pool VALUES('198.18.0.2',true,$1)`, cutoff.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	queue("cf")
	finish("cf", created)
	check("cf", 0, 1)
}
