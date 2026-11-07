package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

func (a *app) saveNodeHealth(ctx context.Context, samplesHash string, started time.Time, candidates []dnsCandidate, states []string) error {
	type proof struct {
		IP     string `json:"ip"`
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	proofs := []proof{}
	for i, state := range states {
		if state != "" {
			proofs = append(proofs, proof{candidates[i].IP, state, candidates[i].Reason})
		}
	}
	if len(proofs) == 0 || samplesHash == "" {
		return nil
	}
	raw, err := json.Marshal(proofs)
	if err != nil {
		return err
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7021081)`); err != nil {
		return err
	}
	var current bool
	err = tx.QueryRow(ctx, `WITH sample_set AS MATERIALIZED (
 SELECT md5(string_agg(config_hash,',' ORDER BY config_hash)) AS hash FROM node_proxy_samples
 ), saved AS (INSERT INTO node_ip_health(ip,samples_hash,status,checked_at,reason)
 SELECT r.ip::inet,$2::text,r.status,$3::timestamptz,r.reason FROM jsonb_to_recordset($1::jsonb) r(ip text,status text,reason text)
 WHERE $2::text=(SELECT hash FROM sample_set)
 ON CONFLICT(ip) DO UPDATE SET samples_hash=excluded.samples_hash,status=excluded.status,
	checked_at=excluded.checked_at,reason=excluded.reason WHERE node_ip_health.checked_at<=excluded.checked_at RETURNING ip)
 SELECT COALESCE(hash=$2::text,false) AND (SELECT count(*) FROM saved)=jsonb_array_length($1::jsonb)
 FROM sample_set`, string(raw), samplesHash, started).Scan(&current)
	if err == nil && !current {
		return fmt.Errorf("检测期间加速节点样本或检测结果已更新，暂停发布并等待重新验证")
	}
	if err != nil {
		return err
	}
	if err = saveHealthRecovery(ctx, tx, "node", samplesHash, started, candidates, states); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Recheck even when DNS sync is disabled. Unknown results expire without
// turning a checker outage into permanent failures; old evidence is retried.
func (a *app) maintainNodeHealth(ctx context.Context) {
	timer := time.NewTicker(5 * time.Minute)
	defer timer.Stop()
	for ctx.Err() == nil {
		if err := a.recheckNodeHealth(ctx); err != nil && ctx.Err() == nil {
			log.Printf("节点可用池复查失败: %v", err)
		}
		if err := a.recheckWebsiteHealth(ctx); err != nil && ctx.Err() == nil {
			log.Printf("网站可用池复查失败: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (a *app) recheckNodeHealth(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	rows, err := a.db.Query(ctx, `SELECT host(p.ip) FROM node_ip_evidence p
 LEFT JOIN node_ip_health h ON h.ip=p.ip WHERE p.protocol='grpc' AND p.available
 AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=p.ip)
 ORDER BY h.checked_at NULLS FIRST,p.ip`)
	if err != nil {
		return err
	}
	items := []dnsCandidate{}
	for rows.Next() {
		var item dnsCandidate
		if err = rows.Scan(&item.IP); err != nil {
			break
		}
		item.State = "healthy"
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	return a.dnsCheckNodes(ctx, items)
}
