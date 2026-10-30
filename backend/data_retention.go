package main

import (
	"context"
	"log"
	"time"
)

// Keep current connectivity samples, including failures used to resume scans.
// Legacy protocol copies and samples for replaced/disabled nodes cannot be used.
func (a *app) pruneConnectivityData(ctx context.Context) (int64, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var removed int64
	for _, query := range []string{
		`DELETE FROM ip_results r WHERE r.protocol<>$1 OR NOT EXISTS (
 SELECT 1 FROM nodes n WHERE n.id=r.node_id AND n.enabled AND n.share_uri<>'' AND md5(n.share_uri)=r.config_hash)`,
		`DELETE FROM node_ip_pool p WHERE p.protocol<>$1 OR NOT EXISTS (
 SELECT 1 FROM nodes n WHERE n.enabled AND n.share_uri<>'' AND md5(n.share_uri)=p.config_hash)`,
	} {
		tag, err := tx.Exec(ctx, query, connectivityProtocol)
		if err != nil {
			return 0, err
		}
		removed += tag.RowsAffected()
	}
	tag, err := tx.Exec(ctx, `DELETE FROM node_scan_rounds WHERE started_at < now()-interval '7 days'
 AND (status NOT IN ('running','waiting') OR updated_at < now()-interval '2 minutes')`)
	if err != nil {
		return 0, err
	}
	removed += tag.RowsAffected()
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
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
