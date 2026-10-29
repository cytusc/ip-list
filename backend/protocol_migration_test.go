package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBackfillEmptyProtocolNetworks(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("requires TEST_DATABASE_URL")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !strings.HasSuffix(db.Config().ConnConfig.Database, "_test") {
		t.Fatal("requires test database")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	// Isolate the upgrade fixture, including its one-time migration marker.
	fixture := fmt.Sprintf("migration_%d", time.Now().UnixNano())
	if _, err = tx.Exec(ctx, "CREATE SCHEMA "+fixture+"; SET LOCAL search_path TO "+fixture+",public"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM app_migrations;
 INSERT INTO networks(name,cidr) VALUES('empty','198.18.94.0/30'),('partial','198.18.94.4/30'),('usable','198.18.94.8/30');
 INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at)
 SELECT 'vless/grpc','198.18.94.0'::inet+i,i=8,0,now() FROM generate_series(0,11) i WHERE i<>7;
 INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at)
 SELECT 'vless/xhttp','198.18.94.0'::inet+i,i=0,0,now() FROM generate_series(0,3) i;`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM protocol_networks").Scan(&count); err != nil || count != 1 {
		t.Fatalf("only fully checked empty pool should be disabled: %d, %v", count, err)
	}
	var protocol, name string
	if err = tx.QueryRow(ctx, "SELECT p.protocol,w.name FROM protocol_networks p JOIN networks w ON w.id=p.network_id").Scan(&protocol, &name); err != nil || protocol != connectivityProtocol || name != "empty" {
		t.Fatal(protocol, name, err)
	}
	// Reinitializing must respect a user's restore instead of reapplying history.
	if _, err = tx.Exec(ctx, "DELETE FROM protocol_networks"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM protocol_networks").Scan(&count); err != nil || count != 0 {
		t.Fatal("migration overrode manual restore", count, err)
	}
}
