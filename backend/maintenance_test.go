package main

import (
	"context"
	"testing"
	"time"
)

func TestMaintenancePoolIsolation(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	config := a.db.Config()
	pool, err := maintenancePool(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if pool.Config().MaxConns != 1 || config.ConnConfig.RuntimeParams["application_name"] == "ip-list-maintenance" {
		t.Fatal("maintenance must be bounded without modifying the foreground configuration")
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var application, lockTimeout, statementTimeout string
	if err = conn.QueryRow(ctx, `SELECT current_setting('application_name'),current_setting('lock_timeout'),current_setting('statement_timeout')`).Scan(&application, &lockTimeout, &statementTimeout); err != nil || application != "ip-list-maintenance" || lockTimeout != "2s" || statementTimeout != "2min" {
		t.Fatalf("maintenance limits: %s %s %s %v", application, lockTimeout, statementTimeout, err)
	}
	work, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var value int
	if err = a.db.QueryRow(work, "SELECT 42").Scan(&value); err != nil || value != 42 {
		t.Fatalf("occupied maintenance pool blocked foreground: %d %v", value, err)
	}
	wait, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if extra, err := pool.Acquire(wait); err == nil {
		extra.Release()
		t.Fatal("maintenance exceeded its single connection")
	}
}

func TestStartupDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	afterStartup(ctx, time.Hour, func(context.Context) { t.Fatal("maintenance started after cancellation") })
	called := false
	afterStartup(context.Background(), 0, func(context.Context) { called = true })
	if !called {
		t.Fatal("maintenance did not start after the delay")
	}
}
