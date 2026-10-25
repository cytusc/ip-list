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
	var builder netipx.IPSetBuilder
	for _, cidr := range cidrs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || prefix != prefix.Masked() || prefix.Addr().Is4In6() {
			return 0, nil, errors.New("网段无效")
		}
		builder.AddPrefix(prefix)
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
	if count.Sign() == 0 {
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
	if len(j.Results) == 0 {
		return nil
	}
	data, err := json.Marshal(j.Results)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(ctx, `INSERT INTO cf_ip_pool(ip,available,delay_ms,http_status,checked_at)
 SELECT r.ip::inet,r.status='success',0,COALESCE(r.http_status,0),$2
 FROM jsonb_to_recordset($1::jsonb) r(ip text,status text,http_status integer)
 ON CONFLICT(ip) DO UPDATE SET available=excluded.available,delay_ms=excluded.delay_ms,http_status=excluded.http_status,checked_at=excluded.checked_at
 WHERE cf_ip_pool.checked_at<=excluded.checked_at`, string(data), j.FinishedAt)
	return err
}
func (a *app) listCFTargets(w http.ResponseWriter, r *http.Request) {
	var data json.RawMessage
	err := a.db.QueryRow(r.Context(), `SELECT COALESCE(json_agg(t),'[]') FROM (SELECT t.*,(SELECT count(*) FROM cf_ip_pool WHERE available) AS available_count FROM cf_targets t ORDER BY created_at DESC) t`).Scan(&data)
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
	var target, items json.RawMessage
	err := a.db.QueryRow(r.Context(), "SELECT row_to_json(t) FROM cf_targets t WHERE id=$1", id).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, 404, "目标地址不存在", nil)
		return
	}
	if err != nil {
		dbError(w, err)
		return
	}
	err = a.db.QueryRow(r.Context(), `SELECT COALESCE(json_agg(r),'[]') FROM (SELECT host(s.ip) AS ip,s.http_status,s.checked_at,COALESCE(info.asn,'') AS asn,COALESCE(info.provider,'') AS provider FROM cf_ip_pool s `+networkInfoJoin+` WHERE s.available ORDER BY s.ip) r`).Scan(&items)
	if err != nil {
		dbError(w, err)
		return
	}
	var job *scanJob
	jobID, err := a.redis.Get(r.Context(), cfLatestKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		apiError(w, 503, "任务服务暂时不可用", nil)
		return
	}
	if err == nil {
		data, e := a.redis.Get(r.Context(), scanKey(jobID)).Bytes()
		if e != nil && !errors.Is(e, redis.Nil) {
			apiError(w, 503, "任务服务暂时不可用", nil)
			return
		}
		if e == nil {
			var j scanJob
			if json.Unmarshal(data, &j) == nil && j.UserID == r.Context().Value(userContextKey{}).(user).ID {
				if j.Status == "running" && time.Since(j.UpdatedAt) > 50*time.Second {
					j.Status = "interrupted"
					j.Error = "任务已中断，请重新测试"
				}
				job = &j
			}
		}
	}
	respond(w, 200, map[string]any{"target": target, "items": items, "job": job})
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
	total, targets, err := cfScanTargets(cidrs)
	if err != nil {
		apiError(w, 422, err.Error(), nil)
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
	in := scanInput{NetworkIDs: input.NetworkIDs, URL: raw, Timeout: input.Timeout, Concurrency: input.Concurrency}
	job := scanJob{ID: jobID, TargetID: id, Input: in, StartedAt: time.Now().UTC(), UserID: r.Context().Value(userContextKey{}).(user).ID, NodeName: name, URL: raw, CIDRs: cidrs, Status: "running", Total: total, Results: []scanResult{}}
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
