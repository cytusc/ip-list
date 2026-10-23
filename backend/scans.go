package main

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
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
return redis.call('exists',KEYS[2])`

type scanInput struct {
	NodeID          string   `json:"node_id"`
	NetworkIDs      []string `json:"network_ids"`
	URL             string   `json:"url"`
	Timeout         int      `json:"timeout_ms"`
	Concurrency     int      `json:"concurrency"`
	IntervalMinutes int      `json:"interval_minutes"`
	FullEvery       int      `json:"full_every"`
}
type scanResult struct {
	IP            string `json:"ip"`
	Status        string `json:"status"`
	Delay         int    `json:"delay_ms"`
	Error         string `json:"error,omitempty"`
	Change        string `json:"change,omitempty"`
	PreviousDelay *int   `json:"previous_delay_ms,omitempty"`
}
type scanJob struct {
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
	template, share, err := parseShare(raw)
	if err != nil {
		apiError(w, 422, "请先为该节点添加有效的 VLESS 链接", nil)
		return
	}
	rows, err := a.db.Query(r.Context(), "SELECT id::text,cidr::text FROM networks WHERE id=ANY($1::uuid[]) AND enabled=true", input.NetworkIDs)
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	cidrs := []string{}
	found := map[string]bool{}
	for rows.Next() {
		var id, cidr string
		if err = rows.Scan(&id, &cidr); err != nil {
			dbError(w, err)
			return
		}
		found[id] = true
		cidrs = append(cidrs, cidr)
	}
	if err = rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	for _, id := range input.NetworkIDs {
		if !found[id] {
			apiError(w, 422, "所选网段已删除或停用，请刷新后重试", nil)
			return
		}
	}
	ips, err := expandPrefixes(cidrs)
	if err != nil {
		apiError(w, 422, err.Error(), nil)
		return
	}
	bytes := make([]byte, 16)
	if _, err = rand.Read(bytes); err != nil {
		apiError(w, 500, "无法创建任务", nil)
		return
	}
	id := hex.EncodeToString(bytes)
	locked, err := a.redis.SetNX(r.Context(), scanLock, id, 45*time.Second).Result()
	if err != nil {
		apiError(w, 503, "任务服务暂时不可用", nil)
		return
	}
	if !locked {
		apiError(w, 409, "已有测试正在运行，请等待完成或停止当前任务", nil)
		return
	}
	job := scanJob{Input: input, Round: 1, Scope: "full", StartedAt: time.Now().UTC(), ID: id, UserID: r.Context().Value(userContextKey{}).(user).ID, NodeName: name, URL: input.URL, CIDRs: cidrs, Status: "running", Total: len(ips), Results: []scanResult{}}
	job.Protocol = share.Query().Get("type")
	if job.Protocol == "" {
		job.Protocol = "tcp"
	}
	// Configuration identity only, not a security hash. PostgreSQL compares md5(share_uri).
	fingerprint := md5.Sum([]byte(raw))
	job.ConfigHash = hex.EncodeToString(fingerprint[:])
	if err = a.persistScan(r.Context(), &job); err == nil {
		err = a.redis.Set(r.Context(), latestKey(job.UserID), id, scanTTL).Err()
	}
	if err != nil {
		a.releaseScan(id)
		apiError(w, 503, "任务保存失败，请重试", nil)
		return
	}
	go a.runScan(job, input, template, ips)
	respond(w, 202, job)
}
func (a *app) persistScan(ctx context.Context, job *scanJob) error {
	job.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return a.redis.Set(ctx, scanKey(job.ID), data, scanTTL).Err()
}
func (a *app) releaseScan(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = a.redis.Eval(ctx, `if redis.call('get',KEYS[1])==ARGV[1] then return redis.call('del',KEYS[1]) end return 0`, []string{scanLock}, id).Err()
}
func (a *app) runScanRound(parent context.Context, job scanJob, input scanInput, template map[string]any, ips []netip.Addr, previous map[string]scanResult) scanJob {
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	defer cancel()
	workerCount := min(input.Concurrency, len(ips))
	work := make(chan netip.Addr, workerCount)
	// At most 4096 entries. Redis latency must not stall active network probes.
	results := make(chan scanResult, len(ips))
	job.Results = make([]scanResult, 0, len(ips))
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
				delay, err := probeIP(probeCtx, template, ip, input.URL)
				stop()
				if ctx.Err() != nil {
					return
				}
				result := scanResult{IP: ip.String(), Status: "success", Delay: delay}
				if err != nil {
					result.Status = "failed"
					result.Error = probeError(err)
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
		for _, ip := range ips {
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
						job.Error = "服务停止或任务超过 30 分钟，请重新测试"
					} else {
						job.Status = "completed"
					}
				}
				finalCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				finished := time.Now().UTC()
				job.FinishedAt = &finished
				if err := a.saveIPResults(finalCtx, job); err != nil {
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
				job.Error = "服务停止或任务超过 30 分钟，请重新测试"
			}
			done = nil // Drain workers before releasing the lease.
		case <-ticker.C:
			checkCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
			control, err := a.redis.Eval(checkCtx, scanControlScript, []string{scanLock, scanKey(job.ID) + ":cancel"}, job.ID).Int()
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
func probeError(err error) string {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "连接或代理请求超时"
	}
	if errors.Is(err, context.Canceled) {
		return "测试已停止"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "certificate") || strings.Contains(message, "tls") {
		return "TLS 握手或证书校验失败"
	}
	return "代理连接失败（端口、传输握手或上游不可用）"
}
func (a *app) readScan(w http.ResponseWriter, r *http.Request, id string) *scanJob {
	if len(id) != 32 {
		apiError(w, 404, "测试任务不存在或已过期", nil)
		return nil
	}
	data, err := a.redis.Get(r.Context(), scanKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		apiError(w, 404, "测试任务不存在或已过期", nil)
		return nil
	}
	if err != nil {
		apiError(w, 503, "任务服务暂时不可用", nil)
		return nil
	}
	var job scanJob
	if err = json.Unmarshal(data, &job); err != nil {
		apiError(w, 503, "任务数据无法读取", nil)
		return nil
	}
	if job.UserID != r.Context().Value(userContextKey{}).(user).ID {
		apiError(w, 404, "测试任务不存在", nil)
		return nil
	}
	if (job.Status == "running" || job.Status == "waiting") && time.Since(job.UpdatedAt) > 50*time.Second {
		job.Status = "interrupted"
		job.Error = "任务进度已中断，请重新测试"
	}
	return &job
}
func (a *app) getScan(w http.ResponseWriter, r *http.Request) {
	if job := a.readScan(w, r, r.PathValue("id")); job != nil {
		respond(w, 200, job)
	}
}
func (a *app) latestScan(w http.ResponseWriter, r *http.Request) {
	id, err := a.redis.Get(r.Context(), latestKey(r.Context().Value(userContextKey{}).(user).ID)).Result()
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
