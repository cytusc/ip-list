package ipguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log"
	"net/netip"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

const (
	engineJobPending = "pending"
	engineJobRunning = "running"
	engineJobDone    = "done"
	engineJobFailed  = "failed"
)

// engineLocal is true inside the worker process. Schedules, patrol and
// automatic scans then execute in-process; the API enqueues instead.
var engineLocal bool

// EnableLocalEngine switches scan launching to in-process execution.
func EnableLocalEngine() { engineLocal = true }

func (a *app) enqueueEngineJob(ctx context.Context, jobID, kind string) error {
	if a.db == nil {
		return errors.New("数据库不可用")
	}
	_, err := a.db.Exec(ctx, `INSERT INTO engine_jobs(job_id,kind) VALUES($1,$2) ON CONFLICT(job_id) DO NOTHING`, jobID, kind)
	return err
}

// RunEngine consumes durable scan jobs. Jobs are claimed with SKIP LOCKED so a
// second worker process cannot execute the same scan twice.
func (a *app) RunEngine(ctx context.Context) {
	if _, err := a.db.Exec(ctx, `UPDATE engine_jobs SET status='failed',finished_at=now(),error='worker restarted' WHERE status='running'`); err != nil {
		log.Printf("重置引擎任务失败: %v", err)
	}
	sem := make(chan struct{}, 4)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		var id, kind string
		err := a.db.QueryRow(ctx, `UPDATE engine_jobs SET status=$1,started_at=now()
		 WHERE job_id=(SELECT job_id FROM engine_jobs WHERE status=$2 ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		 RETURNING job_id,kind`, engineJobRunning, engineJobPending).Scan(&id, &kind)
		if errors.Is(err, pgx.ErrNoRows) {
			select {
		case <-ctx.Done():
			return
			case <-time.After(time.Second):
			}
			continue
		}
		if err != nil {
			log.Printf("领取引擎任务失败: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		sem <- struct{}{}
		go func(jobID, kind string) {
			defer func() { <-sem }()
			err := a.executeEngineJob(ctx, kind, jobID)
			status, message := engineJobDone, ""
			if err != nil {
				status, message = engineJobFailed, err.Error()
			}
			if _, updateErr := a.db.Exec(context.Background(), `UPDATE engine_jobs SET status=$2,finished_at=now(),error=$3 WHERE job_id=$1`, jobID, status, message); updateErr != nil {
				log.Printf("更新引擎任务失败 job=%s error=%q", jobID, updateErr)
			}
		}(id, kind)
	}
}

func (a *app) executeEngineJob(ctx context.Context, kind, jobID string) error {
	raw, err := a.redis.Get(ctx, scanKey(jobID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return errors.New("任务快照不存在或已过期")
	}
	if err != nil {
		return err
	}
	var job scanJob
	if err := json.Unmarshal(raw, &job); err != nil {
		return errors.New("任务快照无法解析")
	}
	switch kind {
	case "node_scan":
		return a.executeNodeScan(ctx, job)
	case "cf_scan":
		return a.executeCFScan(ctx, job)
	default:
		return fmt.Errorf("未知任务类型 %s", kind)
	}
}

func (a *app) executeNodeScan(ctx context.Context, job scanJob) error {
	var shareURI, name string
	err := a.db.QueryRow(ctx, `SELECT share_uri,name FROM nodes WHERE id=$1 AND enabled=true AND share_uri<>''`, job.Input.NodeID).Scan(&shareURI, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		job.Status = "interrupted"
		job.Error = "节点已停用或删除，请重新选择"
		_ = a.persistScan(ctx, &job)
		return errors.New(job.Error)
	}
	if err != nil {
		return err
	}
	template, _, err := parseShare(shareURI)
	if err != nil {
		job.Status = "interrupted"
		job.Error = "节点链接无法解析"
		_ = a.persistScan(ctx, &job)
		return errors.New(job.Error)
	}
	protocol := nodeConnectivityProtocol(shareURI)
	if protocol == "" {
		job.Status = "interrupted"
		job.Error = "连通性检测仅使用 gRPC 节点"
		_ = a.persistScan(ctx, &job)
		return errors.New(job.Error)
	}
	job.Protocol = protocol
	job.NodeName = name
	var ips []netip.Addr
	if job.Scope == "incremental" {
		var checked []string
		checked, err = a.checkedScanIPs(ctx, job.CIDRs, protocol)
		if err == nil {
			var total int
			var targets iter.Seq[netip.Addr]
			total, targets, err = scanTargetsExcluding(job.CIDRs, checked)
			if err == nil {
				ips = slices.Collect(targets)
				job.Total = total
			}
		}
	} else {
		ips, err = a.scanIPsExcludingBlacklist(ctx, job.CIDRs)
		job.Total = len(ips)
	}
	if err != nil {
		job.Status = "interrupted"
		job.Error = err.Error()
		_ = a.persistScan(ctx, &job)
		return err
	}
	job.Completed, job.Success = 0, 0
	job.Results = []scanResult{}
	job.Status = "running"
	_ = a.persistScan(ctx, &job)
	a.runScan(job, job.Input, template, ips)
	return nil
}

func (a *app) executeCFScan(ctx context.Context, job scanJob) error {
	var targetURL, name string
	err := a.db.QueryRow(ctx, `SELECT url,name FROM cf_targets WHERE id=$1`, job.TargetID).Scan(&targetURL, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		job.Status = "interrupted"
		job.Error = "CF 目标已删除"
		_ = a.persistScan(ctx, &job)
		return errors.New(job.Error)
	}
	if err != nil {
		return err
	}
	job.URL = targetURL
	job.NodeName = name
	job.Input.URL = targetURL
	var checked []string
	if job.Scope == "incremental" {
		checked, err = a.checkedScanIPs(ctx, job.CIDRs, "")
	} else {
		checked, err = a.blacklistIPs(ctx, job.CIDRs)
	}
	if err != nil {
		return err
	}
	total, targets, err := scanTargetsExcluding(job.CIDRs, checked)
	if err != nil {
		return err
	}
	job.Total = total
	job.Completed, job.Success = 0, 0
	job.Results = []scanResult{}
	job.Status = "running"
	_ = a.persistScan(ctx, &job)
	defer a.releaseLock(cfLock, job.ID)
	a.runScanTargets(ctx, job, job.Input, nil, total, targets, nil)
	return nil
}
