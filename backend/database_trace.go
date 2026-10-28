package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SQL and arguments may contain credentials or node links. Log only a stable
// fingerprint, so repeated slow statements can be identified without leaking them.
type databaseTracer struct {
	threshold time.Duration
	logf      func(string, ...any)
}

type databaseRequestKey struct{}
type databaseQueryKey struct{}
type databaseAcquireKey struct{}
type databaseQueryTrace struct {
	started time.Time
	id      string
}

func databaseQueryID(sql string) string {
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(sql), " ")))
	return fmt.Sprintf("%x", sum[:8])
}

func databaseRequest(ctx context.Context) string {
	if pattern, ok := ctx.Value(databaseRequestKey{}).(string); ok && pattern != "" {
		return pattern
	}
	return "background"
}

func databaseErrorCode(err error) string {
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &pgErr):
		return pgErr.Code
	default:
		return fmt.Sprintf("%T", err)
	}
}

func (t *databaseTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, databaseQueryKey{}, databaseQueryTrace{time.Now(), databaseQueryID(data.SQL)})
}

func (t *databaseTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	trace, ok := ctx.Value(databaseQueryKey{}).(databaseQueryTrace)
	if !ok {
		return
	}
	elapsed := time.Since(trace.started)
	if elapsed >= t.threshold || data.Err != nil {
		t.logf("数据库查询 query=%s request=%q duration=%s error=%s", trace.id, databaseRequest(ctx), elapsed.Round(time.Millisecond), databaseErrorCode(data.Err))
	}
}

func (t *databaseTracer) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	return context.WithValue(ctx, databaseAcquireKey{}, time.Now())
}

func (t *databaseTracer) TraceAcquireEnd(ctx context.Context, pool *pgxpool.Pool, data pgxpool.TraceAcquireEndData) {
	started, ok := ctx.Value(databaseAcquireKey{}).(time.Time)
	if !ok {
		return
	}
	elapsed := time.Since(started)
	if elapsed >= t.threshold || data.Err != nil {
		stat := pool.Stat()
		t.logf("数据库连接等待 request=%q duration=%s acquired=%d total=%d max=%d error=%s", databaseRequest(ctx), elapsed.Round(time.Millisecond), stat.AcquiredConns(), stat.TotalConns(), stat.MaxConns(), databaseErrorCode(data.Err))
	}
}
