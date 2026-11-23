package ipguard

import (
	"context"
	"log"
	"time"
)

// Keep current connectivity samples, including failures used to resume scans.
// Legacy protocol copies and samples for replaced/disabled nodes cannot be used.
func (a *app) pruneConnectivityData(ctx context.Context) (int64, error) {
	var removed int64
	// Resolve the active configuration set once. The previous correlated
	// NOT EXISTS performed an index probe for every one of the 482k pool rows;
	// this hash anti-join removes the stale rows in a single table scan.
	tag, err := a.db.Exec(ctx, `DELETE FROM node_ip_pool p
 WHERE p.protocol=$1
 AND (p.config_hash IS NULL OR p.config_hash NOT IN (
  SELECT md5(share_uri) FROM nodes WHERE enabled AND share_uri<>''
 ))`, connectivityProtocol)
	if err != nil {
		return 0, err
	}
	removed += tag.RowsAffected()
	tag, err = a.db.Exec(ctx, `DELETE FROM node_scan_rounds WHERE started_at < now()-interval '7 days'
 AND (status NOT IN ('running','waiting') OR updated_at < now()-interval '2 minutes')`)
	if err != nil {
		return 0, err
	}
	removed += tag.RowsAffected()
	// Finished queue rows are only an execution trail; keep a week for diagnosis.
	tag, err = a.db.Exec(ctx, `DELETE FROM engine_jobs
 WHERE status IN ('done','failed') AND finished_at < now()-interval '7 days'`)
	if err != nil {
		return 0, err
	}
	removed += tag.RowsAffected()
	if removed > 0 && a.redis != nil {
		_ = a.redis.Del(ctx, "ipfilter:scan:network-progress").Err()
	}
	return removed, nil
}

func (a *app) maintainConnectivityData(ctx context.Context) {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		cleanup, cancel := context.WithTimeout(ctx, 2*time.Minute)
		removed, err := a.pruneConnectivityData(cleanup)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Printf("清理冗余检测数据失败: %v", err)
		} else if removed > 0 {
			log.Printf("已清理 %d 条过期或失效检测数据", removed)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
