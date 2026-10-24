package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIndependentNetworkMigration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("requires TEST_DATABASE_URL")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, url)
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
	_, err = tx.Exec(ctx, `
 CREATE SCHEMA migration_fixture;
 SET LOCAL search_path TO migration_fixture;
 CREATE TABLE networks (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 name varchar(80) NOT NULL, cidr cidr NOT NULL,
 node_id uuid NOT NULL, action text NOT NULL DEFAULT 'allow',
 enabled boolean NOT NULL, description varchar(500) NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(node_id,cidr));
 INSERT INTO networks(name,cidr,node_id,enabled,description) VALUES
 ('old','192.0.2.0/24',gen_random_uuid(),false,'first'),
 ('new','192.0.2.0/24',gen_random_uuid(),true,'second');`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = tx.Exec(ctx, schema); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var enabled bool
	var notes string
	if err = tx.QueryRow(ctx, "SELECT count(*),bool_or(enabled),max(description) FROM networks").Scan(&count, &enabled, &notes); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !enabled || !strings.Contains(notes, "first") || !strings.Contains(notes, "second") {
		t.Fatalf("migration lost data: %d %v %q", count, enabled, notes)
	}
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_schema='migration_fixture' AND table_name='networks' AND column_name IN ('node_id','action')").Scan(&count); err != nil || count != 0 {
		t.Fatal("legacy fields remain", err)
	}
}
