package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDatabaseQueryTrace(t *testing.T) {
	var output bytes.Buffer
	tracer := &databaseTracer{threshold: time.Hour, logf: log.New(&output, "", 0).Printf}
	ctx := context.WithValue(context.Background(), databaseRequestKey{}, "GET /nodes")
	ctx = tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT $1", Args: []any{"credential"}})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	if output.Len() != 0 {
		t.Fatal("fast successful queries should not be logged")
	}
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "23505", Message: "credential"}})
	entry := output.String()
	if !strings.Contains(entry, databaseQueryID("SELECT $1")) || !strings.Contains(entry, "23505") || !strings.Contains(entry, "GET /nodes") {
		t.Fatalf("missing query diagnostics: %s", entry)
	}
	for _, secret := range []string{"credential", "hidden", "SELECT", "secret"} {
		if strings.Contains(entry, secret) {
			t.Fatalf("query diagnostics expose %s", secret)
		}
	}
	output.Reset()
	tracer.threshold = time.Nanosecond
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	if !strings.Contains(output.String(), "error=none") {
		t.Fatal("slow successful query not logged")
	}
	if databaseQueryID("SELECT\n $1") != databaseQueryID("SELECT $1") {
		t.Fatal("whitespace should not change query fingerprint")
	}
}

func TestDatabaseAcquireTimeoutTrace(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires TEST_DATABASE_URL")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(config.ConnConfig.Database, "_test") {
		t.Fatal("requires test database")
	}
	var output bytes.Buffer
	config.MaxConns = 1
	config.ConnConfig.Tracer = &databaseTracer{threshold: time.Hour, logf: log.New(&output, "", 0).Printf}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = pool.Acquire(ctx); err == nil {
		t.Fatal("acquiring the occupied connection must time out")
	}
	entry := output.String()
	if !strings.Contains(entry, "数据库连接等待") || !strings.Contains(entry, "acquired=1 total=1 max=1 error=deadline") {
		t.Fatalf("missing pool wait diagnostics: %s", entry)
	}
}
