package main

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// Save the range and its queue entries in the same transaction. One gRPC
// sample covers all node protocols; CF keeps its own queue.
func enqueueAutomaticScans(ctx context.Context, tx pgx.Tx, id, cidr, userID string) error {
	rows, err := tx.Query(ctx, "SELECT share_uri FROM nodes WHERE enabled AND share_uri<>''")
	if err != nil {
		return err
	}
	protocols := map[string]bool{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if _, _, parseErr := parseShare(raw); parseErr == nil && nodeConnectivityProtocol(raw) != "" {
			protocols[nodeConnectivityProtocol(raw)] = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	var cf bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM cf_targets)").Scan(&cf); err != nil {
		return err
	}
	if cf {
		protocols["cf"] = true
	}
	for protocol := range protocols {
		_, err = tx.Exec(ctx, `INSERT INTO network_auto_scans(network_id,cidr,protocol,user_id) VALUES($1,$2::cidr,$3,$4)
   ON CONFLICT(network_id,protocol) DO UPDATE SET cidr=excluded.cidr,user_id=excluded.user_id,created_at=now(),recheck_after=NULL`, id, cidr, protocol, userID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *app) maintainAutomaticScans(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		rows, err := a.db.Query(ctx, "SELECT DISTINCT protocol FROM network_auto_scans")
		var protocols []string
		if err == nil {
			for rows.Next() {
				var protocol string
				if err = rows.Scan(&protocol); err != nil {
					break
				}
				protocols = append(protocols, protocol)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("读取自动增量检测队列失败: %v", err)
		}
		for _, protocol := range protocols {
			// Leases are acquired before reading candidates, so repeated ticks and
			// multiple servers cannot create duplicate probes for the same pool.
			go func() {
				if _, err := a.processAutomaticScan(ctx, protocol, ""); err != nil && ctx.Err() == nil {
					log.Printf("自动增量检测失败 pool=%s: %v", protocol, err)
				}
			}()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Patrols call this synchronously while waiting, lending their lease to a
// separate recorded job. The next patrol round waits for it to finish.
func (a *app) processAutomaticScan(ctx context.Context, protocol, leaseID string) (bool, error) {
	if protocol != connectivityProtocol && protocol != "cf" {
		return false, nil
	}
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return false, err
	}
	job := scanJob{ID: hex.EncodeToString(bytes), Round: 1, Protocol: protocol, LeaseID: leaseID, Scope: "incremental", Status: "running", Results: []scanResult{}}
	if protocol == "cf" {
		job.TargetID = "pending"
		job.Protocol = ""
	}
	if leaseID == "" {
		locked, err := a.redis.SetNX(ctx, job.lockKey(), job.ID, 45*time.Second).Result()
		if err != nil || !locked {
			return false, err
		}
		defer a.releaseLock(job.lockKey(), job.ID)
	}
	var networkID, cidr string
	var createdAt time.Time
	var recheckAfter *time.Time
	err := a.db.QueryRow(ctx, `SELECT q.network_id::text,q.cidr::text,q.user_id::text,q.created_at,q.recheck_after
  FROM network_auto_scans q JOIN networks w ON w.id=q.network_id
  WHERE q.protocol=$1 AND w.enabled AND w.cidr=q.cidr
  ORDER BY q.created_at,q.network_id LIMIT 1`, protocol).Scan(&networkID, &cidr, &job.UserID, &createdAt, &recheckAfter)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	input, raw, name, targetID, err := a.automaticScanSample(ctx, protocol)
	if err != nil {
		return false, err
	}
	// Keep requests whose sample has been temporarily disabled queued.
	if name == "" {
		return false, nil
	}
	input.Incremental, input.IntervalMinutes = true, 0
	if recheckAfter != nil {
		input.Incremental = false
		job.Scope = "full"
	}
	input.NetworkIDs = []string{networkID}
	job.Input, job.NodeName, job.URL = input, name, input.URL
	job.CIDRs = []string{cidr}
	var template map[string]any
	if protocol == "cf" {
		job.TargetID = targetID
	} else {
		template, _, err = parseShare(raw)
		if err != nil {
			return false, err
		}
		fingerprint := md5.Sum([]byte(raw))
		job.ConfigHash = hex.EncodeToString(fingerprint[:])
	}
	checked, err := a.checkedScanIPsSince(ctx, job.CIDRs, job.Protocol, recheckAfter)
	if err != nil {
		return false, err
	}
	total, targets, err := scanTargetsExcluding(job.CIDRs, checked)
	if err != nil {
		return false, err
	}
	remove := func() error {
		_, err := a.db.Exec(ctx, "DELETE FROM network_auto_scans WHERE network_id=$1 AND protocol=$2 AND cidr=$3::cidr AND created_at=$4 AND recheck_after IS NOT DISTINCT FROM $5::timestamptz", networkID, protocol, cidr, createdAt, recheckAfter)
		return err
	}
	if total == 0 {
		return false, remove()
	}
	if (protocol != "cf" || recheckAfter != nil) && total > 4096 {
		// Large node ranges are automatically drained in bounded batches.
		batch := make([]netip.Addr, 0, 4096)
		for ip := range targets {
			batch = append(batch, ip)
			if len(batch) == 4096 {
				break
			}
		}
		total, targets = len(batch), slices.Values(batch)
	}
	job.Total, job.StartedAt = total, time.Now().UTC()
	if err = a.persistScan(ctx, &job); err != nil {
		return false, err
	}
	job = a.runScanTargets(ctx, job, input, template, total, targets, nil)
	if job.Status == "cancelled" {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		return true, remove()
	}
	if job.Status != "completed" {
		return true, fmt.Errorf("自动任务 %s: %s", job.ID, job.Error)
	}
	// Re-evaluate persisted results on the next tick. Interrupted tasks and
	// address ranges larger than a node batch keep their remaining addresses.
	return true, nil
}

func (a *app) automaticScanSample(ctx context.Context, protocol string) (scanInput, string, string, string, error) {
	input := scanInput{URL: "https://www.gstatic.com/generate_204", Timeout: 5000, Concurrency: 5}
	if protocol == "cf" {
		input.Concurrency = 16
		preferred := ""
		if data, err := a.redis.Get(ctx, cfLatestKey).Result(); err == nil {
			if snapshot, err := a.redis.Get(ctx, scanKey(data)).Bytes(); err == nil {
				var last scanJob
				if json.Unmarshal(snapshot, &last) == nil {
					input = last.Input
					preferred = last.TargetID
				}
			}
		}
		var id, name, url string
		err := a.db.QueryRow(ctx, `SELECT id::text,name,url FROM cf_targets
   ORDER BY (id::text=$1) DESC,last_checked_at DESC NULLS LAST,created_at,id LIMIT 1`, preferred).Scan(&id, &name, &url)
		if errors.Is(err, pgx.ErrNoRows) {
			return input, "", "", "", nil
		}
		input.URL = url
		normalizeAutomaticInput(&input)
		return input, "", name, id, err
	}
	var saved []byte
	err := a.db.QueryRow(ctx, `SELECT input FROM node_scan_rounds WHERE protocol=$1 ORDER BY started_at DESC,id DESC LIMIT 1`, protocol).Scan(&saved)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return input, "", "", "", err
	}
	if len(saved) > 0 {
		if err = json.Unmarshal(saved, &input); err != nil {
			return input, "", "", "", err
		}
	}
	normalizeAutomaticInput(&input)
	rows, err := a.db.Query(ctx, "SELECT id::text,name,share_uri FROM nodes WHERE enabled AND share_uri<>'' ORDER BY (id::text=$1) DESC,created_at,id", input.NodeID)
	if err != nil {
		return input, "", "", "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, raw string
		if err = rows.Scan(&id, &name, &raw); err != nil {
			return input, "", "", "", err
		}
		if nodeConnectivityProtocol(raw) != protocol {
			continue
		}
		if _, _, err := parseShare(raw); err != nil {
			continue
		}
		input.NodeID = id
		return input, raw, name, "", nil
	}
	return input, "", "", "", rows.Err()
}

func normalizeAutomaticInput(input *scanInput) {
	if input.Timeout < 1000 || input.Timeout > 15000 {
		input.Timeout = 5000
	}
	if input.Concurrency < 1 || input.Concurrency > 32 {
		input.Concurrency = 5
	}
	if input.URL == "" {
		input.URL = "https://www.gstatic.com/generate_204"
	}
}
