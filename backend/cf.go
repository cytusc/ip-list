package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"go4.org/netipx"
)

const cfLock = "ipfilter:cf:active"

// Merge overlapping prefixes before counting or iterating. Large ranges stay
// compact in memory; the worker queue pulls addresses only as they are needed.
func cfScanTargets(cidrs []string) (int, iter.Seq[netip.Addr], error) {
	return scanTargetsExcluding(cidrs, nil)
}

func scanTargetsExcluding(cidrs, checked []string) (int, iter.Seq[netip.Addr], error) {
	var builder netipx.IPSetBuilder
	for _, cidr := range cidrs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || prefix != prefix.Masked() || prefix.Addr().Is4In6() {
			return 0, nil, errors.New("网段无效")
		}
		builder.AddPrefix(prefix)
	}
	for _, raw := range checked {
		ip, err := netip.ParseAddr(raw)
		if err != nil {
			return 0, nil, err
		}
		builder.Remove(ip)
	}
	set, err := builder.IPSet()
	if err != nil {
		return 0, nil, err
	}
	prefixes := set.Prefixes()
	count := new(big.Int)
	for _, prefix := range prefixes {
		count.Add(count, new(big.Int).Lsh(big.NewInt(1), uint(prefix.Addr().BitLen()-prefix.Bits())))
	}
	if count.Sign() == 0 && len(checked) == 0 {
		return 0, nil, errors.New("请选择至少一个网段")
	}
	if !count.IsInt64() || uint64(count.Int64()) > uint64(^uint(0)>>1) {
		return 0, nil, errors.New("网段地址数量超出系统可表示范围")
	}
	return int(count.Int64()), func(yield func(netip.Addr) bool) {
		for _, prefix := range prefixes {
			for ip := prefix.Addr(); ip.IsValid() && prefix.Contains(ip); ip = ip.Next() {
				if !yield(ip) {
					return
				}
			}
		}
	}, nil
}

func (j scanJob) lockKey() string {
	if j.TargetID != "" {
		return cfLock
	}
	if j.Protocol != "" {
		return scanLock + ":" + j.Protocol
	}
	return scanLock
}

const cfLatestKey = "ipfilter:cf:latest"
const cfDoneKey = "ipfilter:cf:done"
const cfDoneKeep = 5

// Keep the original URL (Host and SNI), pin only the TCP endpoint. Never use
// environment proxies or follow redirects to an untested address.
func probeCF(ctx context.Context, ip netip.Addr, raw string, config *tls.Config) (int, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, err
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	transport := &http.Transport{TLSClientConfig: config, ForceAttemptHTTP2: true, DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
		}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "IPGuard-CF-Probe/1.0")
	response, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return response.StatusCode, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return response.StatusCode, nil
}
func (a *app) saveScanResults(ctx context.Context, j scanJob) error {
	if j.TargetID == "" {
		return a.saveIPResults(ctx, j)
	}
	if len(j.Results) == 0 || j.FinishedAt == nil {
		return nil
	}
	data, err := json.Marshal(j.Results)
	if err != nil {
		return err
	}
	success := 0
	for _, result := range j.Results {
		if result.Status == "success" {
			success++
		}
	}
	status := j.Status
	if status == "" {
		status = "completed"
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `INSERT INTO cf_ip_pool(ip,available,delay_ms,http_status,checked_at)
 SELECT r.ip::inet,r.status='success',0,COALESCE(r.http_status,0),$2
 FROM jsonb_to_recordset($1::jsonb) r(ip text,status text,http_status integer)
 ON CONFLICT(ip) DO UPDATE SET available=excluded.available,delay_ms=excluded.delay_ms,http_status=excluded.http_status,checked_at=excluded.checked_at
 WHERE cf_ip_pool.checked_at<=excluded.checked_at`, string(data), j.FinishedAt)
	if err != nil {
		return err
	}
	// The same round is also recorded per domain, so a domain page can show
	// which IPs answered this domain rather than the shared pool.
	_, err = tx.Exec(ctx, `INSERT INTO cf_results(target_id,ip,available,delay_ms,http_status,checked_at)
 SELECT $1,r.ip::inet,r.status='success',0,COALESCE(r.http_status,0),$3
 FROM jsonb_to_recordset($2::jsonb) r(ip text,status text,http_status integer)
 ON CONFLICT(target_id,ip) DO UPDATE SET available=excluded.available,delay_ms=excluded.delay_ms,http_status=excluded.http_status,checked_at=excluded.checked_at
 WHERE cf_results.checked_at<=excluded.checked_at`, j.TargetID, string(data), j.FinishedAt)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO cf_target_rounds(target_id,started_at,finished_at,total,success,status)
 VALUES($1,$2,$3,$4,$5,$6)`, j.TargetID, j.StartedAt, j.FinishedAt, len(j.Results), success, status); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE cf_targets SET last_checked_at=GREATEST(COALESCE(last_checked_at,$2),$2) WHERE id=$1`, j.TargetID, j.FinishedAt); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	// Remember the rounds that finished, so the progress page can still show one
	// after the domain that ran it is deleted.
	if status == "completed" && a.redis != nil {
		_, _ = a.redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.LPush(ctx, cfDoneKey, j.ID)
			pipe.LTrim(ctx, cfDoneKey, 0, cfDoneKeep-1)
			pipe.Expire(ctx, cfDoneKey, scanTTL)
			return nil
		})
	}
	return nil
}

// Per-domain metrics shared by the list and detail queries: current available
// IPs for the domain, availability of the latest round, cumulative
// availability, and the shared pool size. Older per-domain results predate the
// round table, so they act as a fallback.
const cfTargetStats = `(SELECT count(*) FROM cf_results r WHERE r.target_id=t.id AND r.available) AS available_count,
 (SELECT count(*) FROM cf_ip_pool WHERE available) AS shared_count,
 COALESCE(
  (SELECT round(100.0*success/NULLIF(total,0),2) FROM cf_target_rounds k WHERE k.target_id=t.id ORDER BY finished_at DESC LIMIT 1),
  (SELECT round(100.0*count(*) FILTER (WHERE r.available)/NULLIF(count(*),0),2) FROM cf_results r WHERE r.target_id=t.id)
 ) AS availability,
 COALESCE(
  (SELECT round(100.0*sum(success)/NULLIF(sum(total),0),2) FROM cf_target_rounds k WHERE k.target_id=t.id),
  (SELECT round(100.0*count(*) FILTER (WHERE r.available)/NULLIF(count(*),0),2) FROM cf_results r WHERE r.target_id=t.id)
 ) AS overall,
 (SELECT count(*) FROM cf_target_rounds k WHERE k.target_id=t.id) AS rounds,
 COALESCE(
  (SELECT sum(total) FROM cf_target_rounds k WHERE k.target_id=t.id),
  (SELECT count(*) FROM cf_results r WHERE r.target_id=t.id)
 ) AS checked_total,
 COALESCE(
  (SELECT sum(success) FROM cf_target_rounds k WHERE k.target_id=t.id),
  (SELECT count(*) FROM cf_results r WHERE r.target_id=t.id AND r.available)
 ) AS checked_success`

func (a *app) listCFTargets(w http.ResponseWriter, r *http.Request) {
	search := r.URL.Query().Get("search")
	where := ` FROM cf_targets t WHERE ($1='' OR position(lower($1) in lower(t.name||' '||t.url))>0)`
	p, ok := a.listPage(w, r, 50, "SELECT count(*)"+where, search)
	if !ok {
		return
	}
	var data json.RawMessage
	err := a.db.QueryRow(r.Context(), `WITH selected AS MATERIALIZED (SELECT t.*`+where+` ORDER BY t.created_at DESC,t.id DESC LIMIT $2 OFFSET $3) SELECT COALESCE(json_agg(t),'[]') FROM (SELECT t.*,`+cfTargetStats+` FROM selected t ORDER BY t.created_at DESC,t.id DESC) t`, search, p.Size, p.Offset()).Scan(&data)
	if err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, data)
}

func (a *app) createCFTarget(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.URL = strings.TrimSpace(input.URL)
	u, err := url.Parse(input.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(input.URL) > 2048 || len(input.Name) == 0 || len([]rune(input.Name)) > 100 {
		apiError(w, 422, "请输入名称及有效的 HTTPS 网址（不含账号或片段）", nil)
		return
	}
	var id string
	if err = a.db.QueryRow(r.Context(), "INSERT INTO cf_targets(name,url) VALUES($1,$2) ON CONFLICT(url) DO NOTHING RETURNING id::text", input.Name, input.URL).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			apiError(w, 409, "目标地址已存在，请返回列表查看", nil)
			return
		}
		dbError(w, err)
		return
	}
	respond(w, 201, map[string]string{"id": id})
}
func cfID(w http.ResponseWriter, r *http.Request) string {
	id := r.PathValue("id")
	if validate.Var(id, "uuid") != nil {
		apiError(w, 404, "目标地址不存在", nil)
		return ""
	}
	return id
}
func (a *app) getCFTarget(w http.ResponseWriter, r *http.Request) {
	id := cfID(w, r)
	if id == "" {
		return
	}
	var target, items, history json.RawMessage
	err := a.db.QueryRow(r.Context(), "SELECT row_to_json(t) FROM (SELECT t.*,"+cfTargetStats+" FROM cf_targets t WHERE id=$1) t", id).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, 404, "目标地址不存在", nil)
		return
	}
	if err != nil {
		dbError(w, err)
		return
	}
	search := r.URL.Query().Get("search")
	p, ok := a.listPage(w, r, 20, `SELECT count(*) FROM cf_results WHERE target_id=$1 AND available AND ($2='' OR position($2 in host(ip))>0)`, id, search)
	if !ok {
		return
	}
	err = a.db.QueryRow(r.Context(), `WITH selected AS MATERIALIZED (SELECT ip,http_status,checked_at FROM cf_results WHERE target_id=$1 AND available AND ($2='' OR position($2 in host(ip))>0) ORDER BY ip LIMIT $3 OFFSET $4) SELECT COALESCE(json_agg(r),'[]') FROM (SELECT host(s.ip) AS ip,s.http_status,s.checked_at,COALESCE(info.asn,'') AS asn,COALESCE(info.provider,'') AS provider FROM selected s `+networkInfoJoin+` ORDER BY s.ip) r`, id, search, p.Size, p.Offset()).Scan(&items)
	if err != nil {
		dbError(w, err)
		return
	}
	err = a.db.QueryRow(r.Context(), `SELECT COALESCE(json_agg(k),'[]') FROM (SELECT finished_at,total,success,status FROM cf_target_rounds WHERE target_id=$1 ORDER BY finished_at DESC LIMIT 12) k`, id).Scan(&history)
	if err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, map[string]any{"target": target, "items": items, "history": history})
}

// The unified CF scan is one shared job for every domain, so its progress lives
// outside the domain pages.
func (a *app) latestCFScan(w http.ResponseWriter, r *http.Request) {
	ids := []string{}
	id, err := a.redis.Get(r.Context(), cfLatestKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		apiError(w, 503, "任务服务暂时不可用", nil)
		return
	}
	if id != "" {
		ids = append(ids, id)
	}
	// A job whose domain was deleted can no longer describe the current pool;
	// fall back to the most recent round that still belongs to a live domain.
	done, err := a.redis.LRange(r.Context(), cfDoneKey, 0, cfDoneKeep-1).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		apiError(w, 503, "任务服务暂时不可用", nil)
		return
	}
	ids = append(ids, done...)
	for _, id := range ids {
		job, err := a.loadScan(r.Context(), id)
		if err != nil || job.TargetID == "" {
			continue
		}
		var exists bool
		if err = a.db.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM cf_targets WHERE id=$1)", job.TargetID).Scan(&exists); err != nil {
			dbError(w, err)
			return
		}
		if !exists {
			continue
		}
		if job.Status == "running" && time.Since(job.UpdatedAt) > 50*time.Second {
			job.Status = "interrupted"
			job.Error = "任务已中断，请重新测试"
		}
		if r.URL.Query().Get("summary") == "1" {
			job.Results = []scanResult{}
		}
		respond(w, 200, job)
		return
	}
	respond(w, 200, nil)
}

// The shared pool outlives a single job, so it keeps its own listing.
func (a *app) cfIPPool(w http.ResponseWriter, r *http.Request) {
	search := r.URL.Query().Get("search")
	where := ` FROM cf_ip_pool s WHERE s.available AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=s.ip) AND ($1='' OR position($1 in host(s.ip))>0)`
	p, ok := a.listPage(w, r, 20, "SELECT count(*)"+where, search)
	if !ok {
		return
	}
	var data json.RawMessage
	err := a.db.QueryRow(r.Context(), `WITH selected AS MATERIALIZED (SELECT s.ip,s.http_status,s.checked_at`+where+` ORDER BY s.ip LIMIT $2 OFFSET $3) SELECT COALESCE(json_agg(r),'[]') FROM (SELECT host(s.ip) AS ip,s.http_status,s.checked_at,COALESCE(info.asn,'') AS asn,COALESCE(info.provider,'') AS provider FROM selected s `+networkInfoJoin+` ORDER BY s.ip) r`, search, p.Size, p.Offset()).Scan(&data)
	if err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, data)
}

func (a *app) deleteCFTarget(w http.ResponseWriter, r *http.Request) {
	id := cfID(w, r)
	if id == "" {
		return
	}
	result, err := a.db.Exec(r.Context(), "DELETE FROM cf_targets WHERE id=$1", id)
	if err != nil {
		dbError(w, err)
		return
	}
	if result.RowsAffected() == 0 {
		apiError(w, 404, "目标地址不存在", nil)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
func (a *app) startCFScan(w http.ResponseWriter, r *http.Request) {
	id := cfID(w, r)
	if id == "" {
		return
	}
	var input struct {
		Incremental bool     `json:"incremental"`
		NetworkIDs  []string `json:"network_ids"`
		Timeout     int      `json:"timeout_ms"`
		Concurrency int      `json:"concurrency"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.NetworkIDs) == 0 || len(input.NetworkIDs) > 64 || input.Timeout < 1000 || input.Timeout > 15000 || input.Concurrency < 1 || input.Concurrency > 32 {
		apiError(w, 422, "请选择 1–64 个网段；超时 1000–15000 毫秒，并发 1–32", nil)
		return
	}
	for _, v := range input.NetworkIDs {
		if validate.Var(v, "uuid") != nil {
			apiError(w, 422, "网段 ID 无效", nil)
			return
		}
	}
	var raw, name string
	err := a.db.QueryRow(r.Context(), "SELECT url,name FROM cf_targets WHERE id=$1", id).Scan(&raw, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, 404, "目标地址不存在", nil)
		return
	}
	if err != nil {
		dbError(w, err)
		return
	}
	rows, err := a.db.Query(r.Context(), "SELECT id::text,cidr::text FROM networks WHERE id=ANY($1::uuid[]) AND enabled", input.NetworkIDs)
	if err != nil {
		dbError(w, err)
		return
	}
	cidrs := []string{}
	found := map[string]bool{}
	for rows.Next() {
		var nid, cidr string
		if err = rows.Scan(&nid, &cidr); err != nil {
			break
		}
		found[nid] = true
		cidrs = append(cidrs, cidr)
	}
	rows.Close()
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		dbError(w, err)
		return
	}
	for _, nid := range input.NetworkIDs {
		if !found[nid] {
			apiError(w, 422, "网段已删除或停用，请刷新", nil)
			return
		}
	}
	var checked []string
	if input.Incremental {
		checked, err = a.checkedScanIPs(r.Context(), cidrs, "")
		if err != nil {
			dbError(w, err)
			return
		}
	} else {
		checked, err = a.blacklistIPs(r.Context(), cidrs)
		if err != nil {
			dbError(w, err)
			return
		}
	}
	total, targets, err := scanTargetsExcluding(cidrs, checked)
	if err != nil {
		apiError(w, 422, err.Error(), nil)
		return
	}
	if total == 0 {
		apiError(w, 422, "所选网段已全部检测，无新增 IP；如需复测请选择全量检测", nil)
		return
	}
	bytes := make([]byte, 16)
	if _, err = rand.Read(bytes); err != nil {
		apiError(w, 500, "无法创建任务", nil)
		return
	}
	jobID := hex.EncodeToString(bytes)
	locked, err := a.redis.SetNX(r.Context(), cfLock, jobID, 45*time.Second).Result()
	if err != nil {
		apiError(w, 503, "任务服务暂时不可用", nil)
		return
	}
	if !locked {
		apiError(w, 409, "已有 CF 检测正在运行，请等待完成或停止任务", nil)
		return
	}
	in := scanInput{Incremental: input.Incremental, NetworkIDs: input.NetworkIDs, URL: raw, Timeout: input.Timeout, Concurrency: input.Concurrency}
	job := scanJob{ID: jobID, TargetID: id, Input: in, StartedAt: time.Now().UTC(), UserID: r.Context().Value(userContextKey{}).(user).ID, NodeName: name, URL: raw, CIDRs: cidrs, Status: "running", Total: total, Results: []scanResult{}}
	job.Scope = "full"
	if input.Incremental {
		job.Scope = "incremental"
	}
	err = a.persistScan(r.Context(), &job)
	if err != nil {
		a.releaseLock(cfLock, jobID)
		apiError(w, 503, "任务保存失败", nil)
		return
	}
	go func() {
		defer a.releaseLock(cfLock, jobID)
		a.runScanTargets(a.scanContext, job, in, nil, total, targets, nil)
	}()
	respond(w, 202, job)
}
