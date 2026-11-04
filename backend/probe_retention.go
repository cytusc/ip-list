package main

import (
	"context"
	"log"
	"time"
)

// A batch moves samples and updates summaries in one transaction. Additive
// upserts also handle late reports and buckets split across batches safely.
func (a *app) compactProbeSamples(ctx context.Context, limit int) (int64, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(736429102)`).Scan(&locked); err != nil || !locked {
		return 0, err
	}
	var moved int64
	err = tx.QueryRow(ctx, `WITH expired AS (
 SELECT id FROM latency_probe_results
 WHERE checked_at<date_bin('5 minutes',now()-interval '2 hours','2000-01-01'::timestamptz)
 ORDER BY checked_at,id LIMIT $1
 ), removed AS (
 DELETE FROM latency_probe_results r USING expired e WHERE r.id=e.id
 RETURNING r.*
 ), summarized AS (
 INSERT INTO latency_probe_rollups(probe_id,ip,mode,tcp_port,bucket_start,sample_count,success_count,delay_sum,min_delay_ms,max_delay_ms)
 SELECT probe_id,ip,mode,tcp_port,date_bin('5 minutes',checked_at,'2000-01-01'::timestamptz),
 count(*),count(*) FILTER(WHERE success),COALESCE(sum(delay_ms) FILTER(WHERE success),0),
 min(delay_ms) FILTER(WHERE success),max(delay_ms) FILTER(WHERE success)
 FROM removed WHERE checked_at>=date_bin('5 minutes',now()-interval '7 days','2000-01-01'::timestamptz)
 GROUP BY probe_id,ip,mode,tcp_port,5
 ON CONFLICT(probe_id,ip,mode,tcp_port,bucket_start) DO UPDATE SET
 sample_count=latency_probe_rollups.sample_count+excluded.sample_count,
 success_count=latency_probe_rollups.success_count+excluded.success_count,
 delay_sum=latency_probe_rollups.delay_sum+excluded.delay_sum,
 min_delay_ms=LEAST(latency_probe_rollups.min_delay_ms,excluded.min_delay_ms),
 max_delay_ms=GREATEST(latency_probe_rollups.max_delay_ms,excluded.max_delay_ms)
 RETURNING 1
 ) SELECT count(*) FROM removed`, limit).Scan(&moved)
	if err != nil {
		return 0, err
	}
	// Bound expiry work as well; disabled probes are included automatically.
	_, err = tx.Exec(ctx, `DELETE FROM latency_probe_rollups WHERE id IN (
 SELECT id FROM latency_probe_rollups WHERE bucket_start<date_bin('5 minutes',now()-interval '7 days','2000-01-01'::timestamptz)
 ORDER BY bucket_start LIMIT $1)`, limit)
	if err != nil {
		return 0, err
	}
	return moved, tx.Commit(ctx)
}

func (a *app) maintainProbeSamples(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	lastVacuum := time.Time{}
	for {
		work, cancel := context.WithTimeout(ctx, 45*time.Second)
		var total int64
		drained := false
		for work.Err() == nil {
			batch, stop := context.WithTimeout(work, 10*time.Second)
			n, err := a.compactProbeSamples(batch, 20000)
			stop()
			if err != nil {
				// Expiring the pass budget intentionally yields to foreground work.
				if ctx.Err() == nil && work.Err() == nil {
					log.Printf("压缩探针采样失败: %v", err)
				}
				break
			}
			total += n
			if n == 0 {
				drained = true
				break
			}
			// Give foreground queries room during the initial historical backfill.
			select {
			case <-work.Done():
			case <-time.After(100 * time.Millisecond):
			}
		}
		cancel()
		if total > 0 {
			log.Printf("已压缩/清理 %d 条探针原始采样", total)
		}
		if drained && time.Since(lastVacuum) >= 6*time.Hour && ctx.Err() == nil {
			maintenance, stop := context.WithTimeout(ctx, 2*time.Minute)
			_, err := a.db.Exec(maintenance, `VACUUM (ANALYZE) latency_probe_results, latency_probe_rollups`)
			stop()
			if err != nil {
				log.Printf("维护探针采样统计失败: %v", err)
			} else {
				lastVacuum = time.Now()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Recent raw samples and five-minute history are read from one MVCC snapshot.
// Unprocessed historical raw data is grouped on read during initial backfill;
// partial batches and late arrivals are merged with the stored summaries.
const probeHistorySQL = `WITH bounds AS (
 SELECT date_bin('5 minutes',now()-interval '2 hours','2000-01-01'::timestamptz) AS cutoff,
 now()-make_interval(hours=>$3) AS since
 ), probe AS (
 SELECT id,mode,CASE WHEN mode='tcp' THEN tcp_port ELSE 0 END AS port FROM latency_probes WHERE id=$1
 ), historical AS (
 SELECT date_bin('5 minutes',r.checked_at,'2000-01-01'::timestamptz) AS at,
 count(*)::bigint AS total,count(*) FILTER(WHERE success)::bigint AS ok,
 COALESCE(sum(delay_ms) FILTER(WHERE success),0) AS sum_delay,
 min(delay_ms) FILTER(WHERE success) AS low,max(delay_ms) FILTER(WHERE success) AS high
 FROM latency_probe_results r JOIN probe p ON r.probe_id=p.id AND r.mode=p.mode AND r.tcp_port=p.port
 CROSS JOIN bounds b WHERE r.ip=$2::inet AND r.checked_at<b.cutoff
 AND r.checked_at>=date_bin('5 minutes',b.since,'2000-01-01'::timestamptz)
 GROUP BY 1
 UNION ALL
 SELECT r.bucket_start,r.sample_count,r.success_count,r.delay_sum,r.min_delay_ms,r.max_delay_ms
 FROM latency_probe_rollups r JOIN probe p ON r.probe_id=p.id AND r.mode=p.mode AND r.tcp_port=p.port
 CROSS JOIN bounds b WHERE r.ip=$2::inet AND r.bucket_start<b.cutoff
 AND r.bucket_start>=date_bin('5 minutes',b.since,'2000-01-01'::timestamptz)
 ), history AS (
 SELECT 0::bigint AS id,at AS checked_at,COALESCE(sum(sum_delay)/NULLIF(sum(ok),0),0)::float8 AS delay_ms,
 sum(ok)>0 AS success,300 AS bucket_seconds,sum(total)::bigint AS sample_count,
 sum(ok)::bigint AS success_count,sum(sum_delay)::float8 AS delay_sum,min(low) AS min_delay_ms,max(high) AS max_delay_ms
 FROM historical GROUP BY at
 ), recent AS (
 SELECT r.id,r.checked_at,r.delay_ms,r.success,0 AS bucket_seconds,0::bigint AS sample_count,
 0::bigint AS success_count,0::float8 AS delay_sum,NULL::float8 AS min_delay_ms,NULL::float8 AS max_delay_ms
 FROM latency_probe_results r JOIN probe p ON r.probe_id=p.id AND r.mode=p.mode AND r.tcp_port=p.port
 CROSS JOIN bounds b WHERE r.ip=$2::inet AND r.checked_at>=b.cutoff AND r.checked_at>b.since
 AND r.checked_at<=now() ORDER BY r.checked_at DESC,r.id DESC LIMIT 1000
 ) SELECT CASE WHEN bucket_seconds>0 THEN -row_number() OVER(ORDER BY checked_at,id) ELSE id END,
 $2::text,delay_ms,success,checked_at,bucket_seconds,sample_count,success_count,delay_sum,min_delay_ms,max_delay_ms
 FROM (SELECT * FROM history UNION ALL SELECT * FROM recent) combined ORDER BY checked_at,id`
