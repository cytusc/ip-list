package ipguard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

// Env is the exported read-only environment helper used by both entry points.
func Env(key, fallback string) string { return env(key, fallback) }

// InitDB creates the schema and default administrator.
func InitDB(ctx context.Context, db *pgxpool.Pool) error { return initDB(ctx, db) }

// Routes returns the HTTP handler tree of the control API.
func (a *app) Routes() http.Handler { return a.routes() }

// Open builds the shared database, Redis and session state for one process.
// When initialize is true it only runs the schema migration and returns a nil app.
func Open(ctx context.Context, initialize bool) (*app, func(), error) {
	_ = godotenv.Load("../.env")
	_ = godotenv.Load(".env")
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return nil, nil, errors.New("请配置 DATABASE_URL")
	}
	dbConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, nil, err
	}
	slowThreshold, err := time.ParseDuration(env("DB_SLOW_QUERY_THRESHOLD", "500ms"))
	if err != nil || slowThreshold <= 0 {
		return nil, nil, errors.New("DB_SLOW_QUERY_THRESHOLD 必须是正数时长，例如 500ms")
	}
	dbConfig.ConnConfig.Tracer = &databaseTracer{threshold: slowThreshold, logf: log.Printf}
	db, err := pgxpool.NewWithConfig(ctx, dbConfig)
	if err != nil {
		return nil, nil, err
	}
	startupTimeout := 10 * time.Second
	if initialize {
		startupTimeout, err = time.ParseDuration(env("DB_INIT_TIMEOUT", "5m"))
		if err != nil || startupTimeout <= 0 {
			db.Close()
			return nil, nil, errors.New("DB_INIT_TIMEOUT 必须是正数时长，例如 5m")
		}
	}
	startup, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	if err := db.Ping(startup); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("PostgreSQL 连接失败: %w", err)
	}
	if initialize {
		if err := initDB(startup, db); err != nil {
			db.Close()
			return nil, nil, err
		}
		return nil, func() { db.Close() }, nil
	}
	options, err := redis.ParseURL(env("REDIS_URL", "redis://localhost:6379/0"))
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	options.DialTimeout = 3 * time.Second
	options.ReadTimeout = 3 * time.Second
	options.WriteTimeout = 3 * time.Second
	options.MaxRetries = 1
	rdb := redis.NewClient(options)
	if err := rdb.Ping(startup).Err(); err != nil {
		rdb.Close()
		db.Close()
		return nil, nil, fmt.Errorf("Redis 连接失败: %w", err)
	}
	a, err := newApp(db, rdb)
	if err != nil {
		rdb.Close()
		db.Close()
		return nil, nil, err
	}
	a.scanContext = ctx
	return a, func() {
		_ = rdb.Close()
		db.Close()
	}, nil
}

// StartBackground runs every long-lived maintainer that touches mihomo or
// schedules scans. Only the worker entry point calls it.
func (a *app) StartBackground(ctx context.Context) error {
	a.scanContext = ctx
	maintenanceDB, err := maintenancePool(ctx, a.db.Config())
	if err != nil {
		return err
	}
	maintenance := &app{db: maintenanceDB, redis: a.redis}
	go func() {
		<-ctx.Done()
		maintenanceDB.Close()
	}()
	go afterStartup(ctx, 5*time.Minute, maintenance.maintainConnectivityData)
	go a.maintainAvailableIPSpeedTests(ctx)
	go afterStartup(ctx, time.Minute, maintenance.maintainProbeSamples)
	go afterStartup(ctx, 10*time.Minute, maintenance.revalidateLegacyCertificates)
	go a.maintainNodeHealth(ctx)
	go a.resumeNodeSchedules(ctx)
	go a.maintainAutomaticScans(ctx)
	go afterStartup(ctx, 3*time.Minute, maintenance.maintainNetworkMetadata)
	go a.maintainDNS(ctx)
	return nil
}

// SetScanContext lets tests replace the root context of patrol loops.
func (a *app) SetScanContext(ctx context.Context) { a.scanContext = ctx }
