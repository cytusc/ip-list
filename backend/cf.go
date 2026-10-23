package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

const cfLock = "ipfilter:cf:active"

func (j scanJob) lockKey() string {
	if j.TargetID != "" {
		return cfLock
	}
	return scanLock
}
func cfLatest(id string) string { return "ipfilter:cf:latest:" + id }

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
	_, err = a.db.Exec(ctx, `INSERT INTO cf_results(target_id,ip,available,delay_ms,http_status,checked_at)
 SELECT t.id,r.ip::inet,r.status='success',0,COALESCE(r.http_status,0),$3
 FROM jsonb_to_recordset($2::jsonb) r(ip text,status text,http_status integer)
 JOIN cf_targets t ON t.id=$1::uuid
 ON CONFLICT(target_id,ip) DO UPDATE SET available=excluded.available,delay_ms=excluded.delay_ms,http_status=excluded.http_status,checked_at=excluded.checked_at
 WHERE cf_results.checked_at<=excluded.checked_at`, j.TargetID, string(data), j.FinishedAt)
	return err
}
func (a *app) listCFTargets(w http.ResponseWriter, r *http.Request) {
	var data json.RawMessage
	err := a.db.QueryRow(r.Context(), `SELECT COALESCE(json_agg(t),'[]') FROM (SELECT t.*,(SELECT count(*) FROM cf_results r WHERE r.target_id=t.id AND available) AS available_count FROM cf_targets t ORDER BY created_at DESC) t`).Scan(&data)
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
	err = a.db.QueryRow(r.Context(), `SELECT COALESCE(json_agg(r),'[]') FROM (SELECT host(ip) AS ip,http_status,checked_at FROM cf_results WHERE target_id=$1 AND available ORDER BY ip) r`, id).Scan(&items)
	if err != nil {
		dbError(w, err)
		return
	}
	var job *scanJob
	jobID, err := a.redis.Get(r.Context(), cfLatest(id)).Result()
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
	job := scanJob{ID: jobID, TargetID: id, Input: in, StartedAt: time.Now().UTC(), UserID: r.Context().Value(userContextKey{}).(user).ID, NodeName: name, URL: raw, CIDRs: cidrs, Status: "running", Total: len(ips), Results: []scanResult{}}
	if err = a.persistScan(r.Context(), &job); err == nil {
		err = a.redis.Set(r.Context(), cfLatest(id), jobID, scanTTL).Err()
	}
	if err != nil {
		a.releaseLock(cfLock, jobID)
		apiError(w, 503, "任务保存失败", nil)
		return
	}
	go func() { defer a.releaseLock(cfLock, jobID); a.runScanRound(a.scanContext, job, in, nil, ips, nil) }()
	respond(w, 202, job)
}
