package main

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"iter"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

const scanLock = "ipfilter:scan:active"
const scanTTL = 24 * time.Hour
const scanHeartbeat = 10 * time.Second

// Renew the lease and check cancellation in one Redis round trip.
const scanControlScript = `
if redis.call('get',KEYS[1]) ~= ARGV[1] then return -1 end
redis.call('expire',KEYS[1],45)
return redis.call('exists',unpack(KEYS,2))`

type scanInput struct {
	Incremental     bool     `json:"incremental"`
	NodeID          string   `json:"node_id"`
	NetworkIDs      []string `json:"network_ids"`
	URL             string   `json:"url"`
	Timeout         int      `json:"timeout_ms"`
	Concurrency     int      `json:"concurrency"`
	IntervalMinutes int      `json:"interval_minutes"`
	FullEvery       int      `json:"full_every"`
}
type scanResult struct {
	HTTPStatus    int    `json:"http_status,omitempty"`
	IP            string `json:"ip"`
	Status        string `json:"status"`
	Delay         int    `json:"delay_ms"`
	Error         string `json:"error,omitempty"`
	Change        string `json:"change,omitempty"`
	PreviousDelay *int   `json:"previous_delay_ms,omitempty"`
}
type scanJob struct {
	LeaseID    string       `json:"-"`
	TargetID   string       `json:"target_id,omitempty"`
	Protocol   string       `json:"protocol,omitempty"`
	ConfigHash string       `json:"-"`
	ID         string       `json:"id"`
	Input      scanInput    `json:"input"`
	Round      int          `json:"round"`
	Scope      string       `json:"scope"`
	NextRunAt  *time.Time   `json:"next_run_at,omitempty"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
	UserID     string       `json:"user_id"`
	NodeName   string       `json:"node_name"`
	URL        string       `json:"url"`
	CIDRs      []string     `json:"cidrs"`
	Status     string       `json:"status"`
	Total      int          `json:"total"`
	Completed  int          `json:"completed"`
	Success    int          `json:"success"`
	Results    []scanResult `json:"results"`
	Error      string       `json:"error,omitempty"`
	UpdatedAt  time.Time    `json:"updated_at"`
}

func (j scanJob) leaseID() string {
	if j.LeaseID != "" {
		return j.LeaseID
	}
	return j.ID
}

func scanKey(id string) string       { return "ipfilter:scan:job:" + id }
func latestKey(userID string) string { return "ipfilter:scan:latest:" + userID }

func (a *app) startScan(w http.ResponseWriter, r *http.Request) {
	var input scanInput
	if !decode(w, r, &input) {
		return
	}
	if validate.Var(input.NodeID, "uuid") != nil || len(input.NetworkIDs) == 0 || len(input.NetworkIDs) > 64 {
		apiError(w, 422, "请选择节点和 1–64 个网段", nil)
		return
	}
	for _, id := range input.NetworkIDs {
		if validate.Var(id, "uuid") != nil {
			apiError(w, 422, "网段 ID 无效", nil)
			return
		}
	}
	if input.Incremental && input.IntervalMinutes != 0 {
		apiError(w, 422, "增量检测请使用单次检测；自动巡检请使用全量检测", nil)
		return
	}
	if input.IntervalMinutes != 0 && (input.IntervalMinutes < 1 || input.IntervalMinutes > 1440 || input.FullEvery < 1 || input.FullEvery > 288) {
		apiError(w, 422, "巡检间隔为 1–1440 分钟，全量扫描频率为 1–288 轮", nil)
		return
	}
	if input.Timeout < 1000 || input.Timeout > 15000 || input.Concurrency < 1 || input.Concurrency > 32 {
		apiError(w, 422, "超时范围 1000–15000 毫秒，并发范围 1–32", nil)
		return
	}
	target, err := url.Parse(input.URL)
	if err != nil || target.Hostname() == "" || (target.Scheme != "http" && target.Scheme != "https") || target.User != nil || target.Fragment != "" || len(input.URL) > 2048 {
		apiError(w, 422, "检测网址需为有效的 HTTP/HTTPS 地址，不含账号和片段", nil)
		return
	}
	var raw, name string
	err = a.db.QueryRow(r.Context(), "SELECT share_uri,name FROM nodes WHERE id=$1 AND enabled=true", input.NodeID).Scan(&raw, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, 422, "节点不存在或已停用", nil)
		return
	}
	if err != nil {
		dbError(w, err)
		return
	}
	template, _, err := parseShare(raw)
	if err != nil {
		apiError(w, 422, "请先为该节点添加有效的 VLESS 链接", nil)
		return
	}
	protocol := nodePoolProtocol(raw)
	rows, err := a.db.Query(r.Context(), `SELECT w.id::text,w.cidr::text FROM networks w WHERE w.id=ANY($1::uuid[]) AND w.enabled
 AND NOT EXISTS(SELECT 1 FROM protocol_networks p WHERE p.network_id=w.id AND p.cidr=w.cidr AND p.protocol=$2)`, input.NetworkIDs, protocol)
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	cidrs := []string{}
	for rows.Next() {
		var id, cidr string
		if err = rows.Scan(&id, &cidr); err != nil {
			dbError(w, err)
			return
		}
		cidrs = append(cidrs, cidr)
	}
	if err = rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	if len(cidrs) == 0 {
		apiError(w, 422, "所选网段在当前协议池中均已禁用或删除，请恢复网段后重试", nil)
		return
	}
	var ips []netip.Addr
	if input.Incremental {
		var checked []string
		checked, err = a.checkedScanIPs(r.Context(), cidrs, protocol)
		if err == nil {
			var total int
			var targets iter.Seq[netip.Addr]
			total, targets, err = scanTargetsExcluding(cidrs, checked)
			if err == nil && total > 4096 {
				err = errors.New("单次最多检测 4096 个新增 IP，请减少所选网段")
			}
			if err == nil {
				ips = slices.Collect(targets)
			}
		} else {
			dbError(w, err)
			return
		}
	} else {
		ips, err = a.scanIPsExcludingBlacklist(r.Context(), cidrs)
	}
	if err != nil {
		apiError(w, 422, err.Error(), nil)
		return
	}
	if len(ips) == 0 {
		apiError(w, 422, "所选网段已全部检测，无新增 IP；如需复测请选择全量检测", nil)
		return
	}
	job, status, message := a.launchNodeScan(r.Context(), r.Context().Value(userContextKey{}).(user).ID, input, raw, name, template, cidrs, ips, false)
	if message != "" {
		apiError(w, status, message, nil)
		return
	}
	respond(w, status, job)
}

// Rechecks share the normal task lease and persistence, but keep existing patrol schedules.
func (a *app) launchNodeScan(ctx context.Context, userID string, input scanInput, raw, name string, template map[string]any, cidrs []string, ips []netip.Addr, keepSchedule bool) (scanJob, int, string) {
	protocol := nodePoolProtocol(raw)
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return scanJob{}, 500, "无法创建任务"
	}
	id := hex.EncodeToString(bytes)
	locked, err := a.redis.SetNX(ctx, scanLock+":"+protocol, id, 45*time.Second).Result()
	if err != nil {
		return scanJob{}, 503, "任务服务暂时不可用"
	}
	if !locked {
		return scanJob{}, 409, "已有测试正在运行，请等待完成或停止当前任务"
	}
	job := scanJob{Input: input, Round: 1, Scope: "full", StartedAt: time.Now().UTC(), ID: id, UserID: userID, NodeName: name, URL: input.URL, CIDRs: cidrs, Status: "running", Total: len(ips), Results: []scanResult{}}
	if input.Incremental {
		job.Scope = "incremental"
	}
	job.Protocol = protocol
	// Configuration identity only, not a security hash. PostgreSQL compares md5(share_uri).
	fingerprint := md5.Sum([]byte(raw))
	job.ConfigHash = hex.EncodeToString(fingerprint[:])
	if err = a.persistScan(ctx, &job); err == nil {
		err = a.redis.Set(ctx, latestKey(job.UserID), id, scanTTL).Err()
	}
	if err != nil {
		a.releaseLock(job.lockKey(), id)
		a.discardScanRecord(ctx, job.ID)
		return scanJob{}, 503, "任务保存失败，请重试"
	}
	if input.IntervalMinutes > 0 {
		data, _ := json.Marshal(job)
		if _, err = a.db.Exec(ctx, `INSERT INTO node_scan_schedules(protocol,job_id,job) VALUES($1,$2,$3)
   ON CONFLICT(protocol) DO UPDATE SET job_id=excluded.job_id,job=excluded.job,enabled=true,updated_at=now()`, job.Protocol, job.ID, data); err != nil {
			a.releaseLock(job.lockKey(), id)
			a.discardScanRecord(ctx, job.ID)
			return scanJob{}, 500, "任务保存失败，请重试"
		}
	} else if !keepSchedule {
		if _, err = a.db.Exec(ctx, "UPDATE node_scan_schedules SET enabled=false,updated_at=now() WHERE protocol=$1", job.Protocol); err != nil {
			a.releaseLock(job.lockKey(), id)
			a.discardScanRecord(ctx, job.ID)
			return scanJob{}, 500, "任务保存失败，请重试"
		}
	}
	go a.runScan(job, input, template, ips)
	return job, 202, ""
}
func (a *app) persistScan(ctx context.Context, job *scanJob) error {
	job.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	if job.TargetID != "" {
		// Long-running CF tasks must remain discoverable beyond the snapshot TTL.
		_, err = a.redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, scanKey(job.ID), data, scanTTL)
			pipe.Set(ctx, cfLatestKey, job.ID, scanTTL)
			return nil
		})
		return err
	}
	if a.db != nil && job.Input.IntervalMinutes > 0 && job.Protocol != "" {
		// An explicit stop disables the schedule; shutdown/interruption keeps it resumable.
		if _, err = a.db.Exec(ctx, `UPDATE node_scan_schedules SET job=$2,enabled=enabled AND $3,updated_at=now() WHERE protocol=$1 AND job_id=$4`, job.Protocol, data, job.Status != "cancelled", job.ID); err != nil {
			return err
		}
	}
	// Every heartbeat also refreshes the round record, so the record list shows
	// live counters without reading the task snapshot.
	if err = a.saveScanRecord(ctx, job, nil); err != nil {
		return err
	}
	_, err = a.redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, scanKey(job.ID), data, scanTTL)
		pipe.Set(ctx, latestKey(job.UserID), job.ID, scanTTL)
		if job.Protocol != "" {
			pipe.Set(ctx, latestKey(job.UserID)+":"+job.Protocol, job.ID, scanTTL)
		}
		return nil
	})
	return err
}
func (a *app) releaseScan(id string) {
	a.releaseLock(scanLock, id)
}
func (a *app) releaseLock(key, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = a.redis.Eval(ctx, `if redis.call('get',KEYS[1])==ARGV[1] then return redis.call('del',KEYS[1]) end return 0`, []string{key}, id).Err()
}
func (a *app) runScanRound(parent context.Context, job scanJob, input scanInput, template map[string]any, ips []netip.Addr, previous map[string]scanResult) scanJob {
	return a.runScanTargets(parent, job, input, template, len(ips), slices.Values(ips), previous)
}

func (a *app) runScanTargets(parent context.Context, job scanJob, input scanInput, template map[string]any, total int, targets iter.Seq[netip.Addr], previous map[string]scanResult) scanJob {
	ctx, cancel := scanRoundContext(parent)
	defer cancel()
	workerCount := min(input.Concurrency, total)
	// External Xray processes need a smaller memory budget than embedded probes.
	// Limit workers before starting each IP's timeout, so queued IPs still run.
	if job.TargetID == "" && env("PROBE_KERNEL", "mihomo") == "xray" {
		workerCount = min(workerCount, 8)
	}
	work := make(chan netip.Addr, workerCount)
	// Bound queue allocation even when a CF job spans a large address range.
	results := make(chan scanResult, min(total, 4096))
	job.Results = make([]scanResult, 0, min(total, 4096))
	var workers sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ip := range work {
				if ctx.Err() != nil {
					return
				}
				probeCtx, stop := context.WithTimeout(ctx, time.Duration(input.Timeout)*time.Millisecond)
				var delay, code int
				var err error
				if job.TargetID != "" {
					code, err = probeCF(probeCtx, ip, input.URL, nil)
				} else {
					delay, err = probeIP(probeCtx, template, ip, input.URL)
				}
				stop()
				if ctx.Err() != nil {
					return
				}
				result := scanResult{IP: ip.String(), Status: "success", Delay: delay, HTTPStatus: code}
				if err != nil {
					if env("PROBE_DEBUG", "") == "1" {
						// Keep test URL query parameters out of diagnostic logs.
						detail := strings.ReplaceAll(err.Error(), input.URL, "[test-url]")
						log.Printf("probe failed job=%s protocol=%s ip=%s error=%q", job.ID, job.Protocol, ip, detail)
					}
					result.Status = "failed"
					result.Error = probeError(err)
					if job.TargetID != "" {
						result.Error = "HTTPS 请求失败或超时"
						if code >= 400 {
							result.Error = "网站返回 HTTP 错误状态"
						}
					}
				}
				select {
				case results <- result:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(work)
		for ip := range targets {
			select {
			case work <- ip:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { workers.Wait(); close(results) }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	savedCompleted := job.Completed
	done := ctx.Done()
	for {
		select {
		case result, ok := <-results:
			if !ok {
				if job.Status == "running" {
					if ctx.Err() != nil {
						job.Status = "interrupted"
						job.Error = scanRoundInterruptedMessage()
					} else {
						job.Status = "completed"
					}
				}
				finalCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				finished := time.Now().UTC()
				job.FinishedAt = &finished
				if err := a.saveScanResults(finalCtx, job); err != nil {
					job.Status = "interrupted"
					job.Error = "可用 IP 列表保存失败，请重新测试"
				}
				if input.IntervalMinutes > 0 && job.Status == "completed" {
					return job
				}
				if err := a.persistScan(finalCtx, &job); err != nil {
					job.Status = "interrupted"
					job.Error = "结果保存失败，请重新测试"
				}
				return job
			}
			result = compareResult(result, previous)
			job.Results = append(job.Results, result)
			job.Completed++
			if result.Status == "success" {
				job.Success++
			}
		case <-done:
			if job.Status == "running" {
				job.Status = "interrupted"
				job.Error = scanRoundInterruptedMessage()
			}
			done = nil // Drain workers before releasing the lease.
		case <-ticker.C:
			checkCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
			control, err := a.redis.Eval(checkCtx, scanControlScript, []string{job.lockKey(), scanKey(job.ID) + ":cancel", scanKey(job.leaseID()) + ":cancel"}, job.leaseID()).Int()
			if err != nil || control < 0 {
				job.Status = "interrupted"
				job.Error = "任务租约失效或 Redis 连接中断，请重试"
				cancel()
			} else if control > 0 && job.Status == "running" {
				job.Status = "cancelled"
				cancel()
			}
			// Publish changed progress at most once per second. While waiting for
			// connections, a heartbeat is sufficient to keep stale-task detection valid.
			if job.Completed != savedCompleted || job.Status != "running" || time.Since(job.UpdatedAt) >= scanHeartbeat {
				if err = a.persistScan(checkCtx, &job); err != nil {
					job.Status = "interrupted"
					job.Error = "Redis 连接中断，请重试"
					cancel()
				} else {
					savedCompleted = job.Completed
				}
			}
			stop()
		}
	}
}

func scanRoundContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}

func scanRoundInterruptedMessage() string {
	return "服务停止，请重新测试"
}
func probeError(err error) string {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "连接或代理请求超时"
	}
	if errors.Is(err, context.Canceled) {
		return "测试已停止"
	}
	message := strings.ToLower(err.Error())
	if strings.HasPrefix(err.Error(), "Xray ") {
		return "Xray 启动或节点配置加载失败"
	}
	if strings.Contains(message, "unexpected response version") {
		return "VLESS 响应版本无效（代理握手失败）"
	}
	if strings.Contains(message, "certificate") || strings.Contains(message, "tls") {
		return "TLS 握手或证书校验失败"
	}
	return "代理连接失败（端口、传输握手或上游不可用）"
}

var (
	errScanNotFound = errors.New("scan not found")
	errScanBroken   = errors.New("scan unreadable")
)

// loadScan returns the task snapshot of the current user, or a sentinel error so
// callers can decide between an HTTP error and a silent fallback.
func (a *app) loadScan(ctx context.Context, id string) (*scanJob, error) {
	if len(id) != 32 {
		return nil, errScanNotFound
	}
	data, err := a.redis.Get(ctx, scanKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, errScanNotFound
	}
	if err != nil {
		return nil, err
	}
	var job scanJob
	if err = json.Unmarshal(data, &job); err != nil {
		return nil, errScanBroken
	}
	if job.UserID != ctx.Value(userContextKey{}).(user).ID {
		return nil, errScanNotFound
	}
	if (job.Status == "running" || job.Status == "waiting") && time.Since(job.UpdatedAt) > 50*time.Second {
		job.Status = "interrupted"
		job.Error = "任务进度已中断，请重新测试"
	}
	return &job, nil
}
func (a *app) readScan(w http.ResponseWriter, r *http.Request, id string) *scanJob {
	job, err := a.loadScan(r.Context(), id)
	if errors.Is(err, errScanNotFound) {
		apiError(w, 404, "测试任务不存在或已过期", nil)
		return nil
	}
	if errors.Is(err, errScanBroken) {
		apiError(w, 503, "任务数据无法读取", nil)
		return nil
	}
	if err != nil {
		apiError(w, 503, "任务服务暂时不可用", nil)
		return nil
	}
	return job
}
func (a *app) getScan(w http.ResponseWriter, r *http.Request) {
	if job := a.readScan(w, r, r.PathValue("id")); job != nil {
		respond(w, 200, job)
	}
}
func (a *app) latestScan(w http.ResponseWriter, r *http.Request) {
	key := latestKey(r.Context().Value(userContextKey{}).(user).ID)
	if protocol := r.URL.Query().Get("protocol"); protocol != "" {
		key += ":" + protocol
	}
	id, err := a.redis.Get(r.Context(), key).Result()
	if errors.Is(err, redis.Nil) {
		respond(w, 200, nil)
		return
	}
	if err != nil {
		apiError(w, 503, "任务服务暂时不可用", nil)
		return
	}
	if job := a.readScan(w, r, id); job != nil {
		respond(w, 200, job)
	}
}
func (a *app) cancelScan(w http.ResponseWriter, r *http.Request) {
	job := a.readScan(w, r, r.PathValue("id"))
	if job == nil {
		return
	}
	if job.TargetID == "" && job.Input.IntervalMinutes > 0 {
		if _, err := a.db.Exec(r.Context(), "UPDATE node_scan_schedules SET enabled=false WHERE protocol=$1 AND job_id=$2", job.Protocol, job.ID); err != nil {
			dbError(w, err)
			return
		}
	}
	if job.Status != "running" && job.Status != "waiting" {
		respond(w, 200, map[string]bool{"ok": true})
		return
	}
	if err := a.redis.Set(r.Context(), scanKey(job.ID)+":cancel", "1", scanTTL).Err(); err != nil {
		apiError(w, 503, "停止失败，请重试", nil)
		return
	}
	respond(w, 200, map[string]string{"status": "cancelling"})
}
