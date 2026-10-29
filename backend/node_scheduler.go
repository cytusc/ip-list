package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"log"
	"time"
)

// Schedules live in PostgreSQL. Redis leases prevent duplicate workers across
// service restarts and multiple instances, independently for each protocol pool.
func (a *app) resumeNodeSchedules(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := a.resumeDueNodeSchedules(ctx); err != nil && ctx.Err() == nil {
			log.Printf("恢复协议池巡检失败: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *app) resumeDueNodeSchedules(ctx context.Context) error {
	rows, err := a.db.Query(ctx, "SELECT job FROM node_scan_schedules WHERE enabled ORDER BY updated_at")
	if err != nil {
		return err
	}
	jobs := []scanJob{}
	for rows.Next() {
		var raw []byte
		var job scanJob
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &job); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, job)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.Protocol != connectivityProtocol {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if job.NextRunAt != nil && time.Now().Before(*job.NextRunAt) {
			continue
		}
		cidrs, err := a.protocolRanges(ctx, job.Input.NetworkIDs, job.Protocol)
		if err != nil {
			return err
		}
		if len(cidrs) == 0 {
			continue
		}
		ips, err := a.scanIPsExcludingBlacklist(ctx, cidrs)
		if err != nil {
			continue
		}
		// The sample may be replaced or deleted; another node of the same pool can
		// carry on scanning without discarding the common results.
		nodes, err := a.db.Query(ctx, "SELECT id::text,name,share_uri FROM nodes WHERE enabled AND share_uri<>'' ORDER BY (id=$1::uuid) DESC,created_at", job.Input.NodeID)
		if err != nil {
			return err
		}
		raw, name, nodeID := "", "", ""
		for nodes.Next() {
			var id, n, u string
			if err = nodes.Scan(&id, &n, &u); err != nil {
				break
			}
			if nodeConnectivityProtocol(u) == job.Protocol {
				nodeID, name, raw = id, n, u
				break
			}
		}
		if err == nil {
			err = nodes.Err()
		}
		nodes.Close()
		if err != nil {
			return err
		}
		if raw == "" {
			continue
		}
		template, _, err := parseShare(raw)
		if err != nil {
			continue
		}
		locked, err := a.redis.SetNX(ctx, job.lockKey(), job.ID, 45*time.Second).Result()
		if err != nil {
			return err
		}
		if !locked {
			continue
		}
		// Recheck explicit stop after acquiring the lease.
		var enabled bool
		if err = a.db.QueryRow(ctx, "SELECT enabled FROM node_scan_schedules WHERE protocol=$1 AND job_id=$2", job.Protocol, job.ID).Scan(&enabled); err != nil || !enabled {
			a.releaseLock(job.lockKey(), job.ID)
			continue
		}
		job.Input.NodeID, job.NodeName = nodeID, name
		fingerprint := md5.Sum([]byte(raw))
		job.ConfigHash = hex.EncodeToString(fingerprint[:])
		job.Round++
		job.Scope = "full"
		job.Status = "running"
		job.CIDRs = cidrs
		job.StartedAt = time.Now().UTC()
		job.NextRunAt = nil
		job.FinishedAt = nil
		job.Total = len(ips)
		job.Completed = 0
		job.Success = 0
		job.Results = []scanResult{}
		job.Error = ""
		if err = a.persistScan(ctx, &job); err != nil {
			a.releaseLock(job.lockKey(), job.ID)
			return err
		}
		go a.runScan(job, job.Input, template, ips)
	}
	return nil
}
