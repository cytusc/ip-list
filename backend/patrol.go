package main

import (
	"context"
	"net/netip"
	"time"
)

// A patrol is one explicitly started task. It owns the existing global lease
// until stopped, and never overlaps rounds or accumulates overdue work.
func (a *app) runScan(job scanJob, input scanInput, template map[string]any, ips []netip.Addr) {
	a.runPatrol(job, input, template, ips, time.Duration(input.IntervalMinutes)*time.Minute)
}

func (a *app) runPatrol(job scanJob, input scanInput, template map[string]any, all []netip.Addr, interval time.Duration) {
	defer a.releaseScan(job.ID)
	previous := make(map[string]scanResult, len(all))
	targets := all
	for {
		job = a.runScanRound(a.scanContext, job, input, template, targets, previous)
		if interval == 0 || job.Status != "completed" {
			return
		}
		for _, result := range job.Results {
			previous[result.IP] = result
		}
		next := time.Now().UTC().Add(interval)
		job.NextRunAt = &next
		job.Status = "waiting"
		if !a.waitPatrol(&job) {
			return
		}
		job.Round++
		targets, job.Scope = patrolTargets(all, previous, job.Round, input.FullEvery)
		job.Status = "running"
		job.Total, job.Completed, job.Success = len(targets), 0, 0
		job.Results = []scanResult{}
		job.Error = ""
		job.NextRunAt, job.FinishedAt = nil, nil
		job.StartedAt = time.Now().UTC()
		if !a.savePatrol(&job) {
			return
		}
	}
}

func patrolTargets(all []netip.Addr, previous map[string]scanResult, round, fullEvery int) ([]netip.Addr, string) {
	if round <= 1 || fullEvery <= 1 || (round-1)%fullEvery == 0 {
		return all, "full"
	}
	ips := make([]netip.Addr, 0, len(previous))
	for _, ip := range all {
		if previous[ip.String()].Status == "success" {
			ips = append(ips, ip)
		}
	}
	// An empty usable set must not cause empty patrols forever.
	if len(ips) == 0 {
		return all, "full"
	}
	return ips, "available"
}

func compareResult(result scanResult, previous map[string]scanResult) scanResult {
	old, exists := previous[result.IP]
	if !exists {
		if result.Status == "success" {
			result.Change = "new"
		}
		return result
	}
	if old.Status == "success" {
		delay := old.Delay
		result.PreviousDelay = &delay
		if result.Status == "failed" {
			result.Change = "lost"
		}
	} else if result.Status == "success" {
		result.Change = "recovered"
	}
	return result
}

func (a *app) savePatrol(job *scanJob) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return a.persistScan(ctx, job) == nil
}

func (a *app) waitPatrol(job *scanJob) bool {
	if !a.savePatrol(job) {
		return false
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	timer := time.NewTimer(time.Until(*job.NextRunAt))
	defer timer.Stop()
	for {
		select {
		case <-a.scanContext.Done():
			job.Status, job.Error = "interrupted", "服务已停止，请重新启动巡检"
		case <-ticker.C:
		case <-timer.C:
		}
		// Check the lease and stop flag before launching a new round, too.
		if job.Status == "waiting" {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			control, err := a.redis.Eval(ctx, scanControlScript, []string{scanLock, scanKey(job.ID) + ":cancel"}, job.ID).Int()
			cancel()
			if err != nil || control < 0 {
				job.Status, job.Error = "interrupted", "任务租约失效或 Redis 连接中断，请重新启动巡检"
			} else if control > 0 {
				job.Status = "cancelled"
			}
		}
		if job.Status != "waiting" {
			job.NextRunAt = nil
			a.savePatrol(job)
			return false
		}
		if !time.Now().Before(*job.NextRunAt) {
			return true
		}
		if time.Since(job.UpdatedAt) >= scanHeartbeat && !a.savePatrol(job) {
			return false
		}
	}
}
