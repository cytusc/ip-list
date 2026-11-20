package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func maintenancePool(ctx context.Context, config *pgxpool.Config) (*pgxpool.Pool, error) {
	config = config.Copy()
	config.MaxConns, config.MinConns, config.MinIdleConns = 1, 0, 0
	config.ConnConfig.RuntimeParams["application_name"] = "ip-list-maintenance"
	config.ConnConfig.RuntimeParams["lock_timeout"] = "2s"
	config.ConnConfig.RuntimeParams["statement_timeout"] = "120s"
	return pgxpool.NewWithConfig(ctx, config)
}

// Let health checks and DNS recover before starting nonessential maintenance.
func afterStartup(ctx context.Context, delay time.Duration, run func(context.Context)) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		if ctx.Err() == nil {
			run(ctx)
		}
	}
}
