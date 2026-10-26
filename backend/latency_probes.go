package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type latencyProbe struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	IntervalSeconds int        `json:"interval_seconds"`
	TimeoutMS       int        `json:"timeout_ms"`
	Concurrency     int        `json:"concurrency"`
	InterfaceName   string     `json:"interface_name"`
	Enabled         bool       `json:"enabled"`
	LastSeen        *time.Time `json:"last_seen"`
	CreatedAt       time.Time  `json:"created_at"`
	Online          bool       `json:"online"`
}

type latencyProbeInput struct {
	Name            string `json:"name"`
	IntervalSeconds int    `json:"interval_seconds"`
	TimeoutMS       int    `json:"timeout_ms"`
	Concurrency     int    `json:"concurrency"`
	InterfaceName   string `json:"interface_name"`
	Enabled         bool   `json:"enabled"`
}

type latencyProbeResult struct {
	ID        int64     `json:"id"`
	IP        string    `json:"ip"`
	DelayMS   float64   `json:"delay_ms"`
	Success   bool      `json:"success"`
	CheckedAt time.Time `json:"checked_at"`
}

type latencyProbeReport struct {
	IP        string    `json:"ip"`
	DelayMS   float64   `json:"delay_ms"`
	Success   bool      `json:"success"`
	CheckedAt time.Time `json:"checked_at"`
}

func newProbeToken() (string, []byte, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	digest := sha256.Sum256([]byte(token))
	return token, digest[:], nil
}

func probeTokenHash(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return digest[:]
}

func validProbeInput(input *latencyProbeInput, create bool) map[string]string {
	fields := map[string]string{}
	input.Name = strings.TrimSpace(input.Name)
	input.InterfaceName = strings.TrimSpace(input.InterfaceName)
	if input.Name == "" || len([]rune(input.Name)) > 80 {
		fields["name"] = "名称长度需为 1–80 个字符"
	}
	if input.IntervalSeconds < 15 || input.IntervalSeconds > 86400 {
		fields["interval_seconds"] = "检测间隔需为 15–86400 秒"
	}
	if input.TimeoutMS < 250 || input.TimeoutMS > 10000 {
		fields["timeout_ms"] = "超时需为 250–10000 毫秒"
	}
	if input.Concurrency < 1 || input.Concurrency > 512 {
		fields["concurrency"] = "并发数需为 1–512"
	}
	if len(input.InterfaceName) > 100 {
		fields["interface_name"] = "网卡名称不能超过 100 个字符"
	}
	if create && !input.Enabled {
		input.Enabled = true
	}
	return fields
}

func (a *app) listLatencyProbes(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `SELECT id::text,name,interval_seconds,timeout_ms,concurrency,
interface_name,enabled,last_seen,created_at,
(enabled AND last_seen>now()-make_interval(secs=>interval_seconds*3)) AS online
FROM latency_probes ORDER BY created_at DESC`)
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	items := []latencyProbe{}
	for rows.Next() {
		var item latencyProbe
		if err = rows.Scan(&item.ID, &item.Name, &item.IntervalSeconds, &item.TimeoutMS,
			&item.Concurrency, &item.InterfaceName, &item.Enabled, &item.LastSeen,
			&item.CreatedAt, &item.Online); err != nil {
			dbError(w, err)
			return
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	respond(w, http.StatusOK, items)
}

func (a *app) createLatencyProbe(w http.ResponseWriter, r *http.Request) {
	var input latencyProbeInput
	if !decode(w, r, &input) {
		return
	}
	if fields := validProbeInput(&input, true); len(fields) != 0 {
		apiError(w, http.StatusUnprocessableEntity, "请检查探针配置", fields)
		return
	}
	token, tokenHash, err := newProbeToken()
	if err != nil {
		dbError(w, err)
		return
	}
	var item latencyProbe
	err = a.db.QueryRow(r.Context(), `INSERT INTO latency_probes
(name,token_hash,interval_seconds,timeout_ms,concurrency,interface_name,enabled)
VALUES($1,$2,$3,$4,$5,$6,true)
RETURNING id::text,name,interval_seconds,timeout_ms,concurrency,interface_name,enabled,last_seen,created_at,false`,
		input.Name, tokenHash, input.IntervalSeconds, input.TimeoutMS, input.Concurrency, input.InterfaceName).
		Scan(&item.ID, &item.Name, &item.IntervalSeconds, &item.TimeoutMS, &item.Concurrency,
			&item.InterfaceName, &item.Enabled, &item.LastSeen, &item.CreatedAt, &item.Online)
	if err != nil {
		dbError(w, err)
		return
	}
	respond(w, http.StatusCreated, map[string]any{"probe": item, "token": token})
}

func (a *app) updateLatencyProbe(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if validate.Var(id, "uuid") != nil {
		apiError(w, http.StatusBadRequest, "探针 ID 无效", nil)
		return
	}
	var input latencyProbeInput
	if !decode(w, r, &input) {
		return
	}
	if fields := validProbeInput(&input, false); len(fields) != 0 {
		apiError(w, http.StatusUnprocessableEntity, "请检查探针配置", fields)
		return
	}
	var item latencyProbe
	err := a.db.QueryRow(r.Context(), `UPDATE latency_probes SET name=$2,interval_seconds=$3,
timeout_ms=$4,concurrency=$5,interface_name=$6,enabled=$7 WHERE id=$1
RETURNING id::text,name,interval_seconds,timeout_ms,concurrency,interface_name,enabled,last_seen,created_at,
(enabled AND last_seen>now()-make_interval(secs=>interval_seconds*3))`, id, input.Name,
		input.IntervalSeconds, input.TimeoutMS, input.Concurrency, input.InterfaceName, input.Enabled).
		Scan(&item.ID, &item.Name, &item.IntervalSeconds, &item.TimeoutMS, &item.Concurrency,
			&item.InterfaceName, &item.Enabled, &item.LastSeen, &item.CreatedAt, &item.Online)
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, http.StatusNotFound, "探针不存在", nil)
		return
	}
	if err != nil {
		dbError(w, err)
		return
	}
	respond(w, http.StatusOK, item)
}

func (a *app) deleteLatencyProbe(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if validate.Var(id, "uuid") != nil {
		apiError(w, http.StatusBadRequest, "探针 ID 无效", nil)
		return
	}
	result, err := a.db.Exec(r.Context(), "DELETE FROM latency_probes WHERE id=$1", id)
	if err != nil {
		dbError(w, err)
		return
	}
	if result.RowsAffected() == 0 {
		apiError(w, http.StatusNotFound, "探针不存在", nil)
		return
	}
	respond(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *app) rotateLatencyProbeToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if validate.Var(id, "uuid") != nil {
		apiError(w, http.StatusBadRequest, "探针 ID 无效", nil)
		return
	}
	token, tokenHash, err := newProbeToken()
	if err != nil {
		dbError(w, err)
		return
	}
	result, err := a.db.Exec(r.Context(), "UPDATE latency_probes SET token_hash=$2 WHERE id=$1", id, tokenHash)
	if err != nil {
		dbError(w, err)
		return
	}
	if result.RowsAffected() == 0 {
		apiError(w, http.StatusNotFound, "探针不存在", nil)
		return
	}
	respond(w, http.StatusOK, map[string]string{"token": token})
}

func (a *app) latencyProbeHistory(w http.ResponseWriter, r *http.Request) {
	id, ip := r.PathValue("id"), r.URL.Query().Get("ip")
	if validate.Var(id, "uuid") != nil {
		apiError(w, http.StatusBadRequest, "探针 ID 无效", nil)
		return
	}
	canonical, ok := canonicalIP(ip)
	if !ok {
		apiError(w, http.StatusBadRequest, "IP 地址无效", nil)
		return
	}
	hours := 24
	if raw := r.URL.Query().Get("hours"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 168 {
			apiError(w, http.StatusBadRequest, "时间范围需为 1–168 小时", nil)
			return
		}
		hours = parsed
	}
	var exists bool
	if err := a.db.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM latency_probes WHERE id=$1)", id).Scan(&exists); err != nil {
		dbError(w, err)
		return
	}
	if !exists {
		apiError(w, http.StatusNotFound, "探针不存在", nil)
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT id,host(ip),delay_ms,success,checked_at
FROM latency_probe_results WHERE probe_id=$1 AND ip=$2::inet AND checked_at>now()-make_interval(hours=>$3)
ORDER BY checked_at`, id, canonical, hours)
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	items := []latencyProbeResult{}
	for rows.Next() {
		var item latencyProbeResult
		if err = rows.Scan(&item.ID, &item.IP, &item.DelayMS, &item.Success, &item.CheckedAt); err != nil {
			dbError(w, err)
			return
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	respond(w, http.StatusOK, items)
}

func (a *app) authorizeLatencyProbe(r *http.Request) (string, bool) {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) < 8 || !strings.EqualFold(value[:7], "Bearer ") {
		return "", false
	}
	token := strings.TrimSpace(value[7:])
	if len(token) < 32 || len(token) > 128 {
		return "", false
	}
	var id string
	err := a.db.QueryRow(r.Context(), `SELECT id::text FROM latency_probes
WHERE token_hash=$1 AND enabled`, probeTokenHash(token)).Scan(&id)
	return id, err == nil
}

func (a *app) probeAgentTargets(w http.ResponseWriter, r *http.Request) {
	id, ok := a.authorizeLatencyProbe(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "探针密钥无效或已停用", nil)
		return
	}
	var interval, timeout, concurrency int
	var interfaceName string
	err := a.db.QueryRow(r.Context(), `UPDATE latency_probes SET last_seen=now() WHERE id=$1
RETURNING interval_seconds,timeout_ms,concurrency,interface_name`, id).
		Scan(&interval, &timeout, &concurrency, &interfaceName)
	if err != nil {
		dbError(w, err)
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT DISTINCT host(p.ip) FROM node_ip_pool p
WHERE p.available AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=p.ip)
ORDER BY p.ip`)
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	ips := []string{}
	for rows.Next() {
		var ip string
		if err = rows.Scan(&ip); err != nil {
			dbError(w, err)
			return
		}
		ips = append(ips, ip)
	}
	if err = rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"ips": ips, "interval_seconds": interval,
		"timeout_ms": timeout, "concurrency": concurrency, "interface_name": interfaceName,
		"retention_days": 30})
}

func (a *app) reportProbeResults(w http.ResponseWriter, r *http.Request) {
	id, ok := a.authorizeLatencyProbe(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "探针密钥无效或已停用", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var input struct {
		Results []latencyProbeReport `json:"results"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		apiError(w, http.StatusBadRequest, "上报数据格式错误", nil)
		return
	}
	if len(input.Results) == 0 || len(input.Results) > 10000 {
		apiError(w, http.StatusUnprocessableEntity, "每批需包含 1–10000 条检测结果", nil)
		return
	}
	seen := map[string]struct{}{}
	for i := range input.Results {
		item := &input.Results[i]
		canonical, valid := canonicalIP(item.IP)
		if !valid || math.IsNaN(item.DelayMS) || math.IsInf(item.DelayMS, 0) || item.DelayMS < 0 || item.DelayMS > 60000 {
			apiError(w, http.StatusUnprocessableEntity, "检测结果包含无效 IP 或延迟", nil)
			return
		}
		item.IP = canonical
		if _, duplicate := seen[canonical]; duplicate {
			apiError(w, http.StatusUnprocessableEntity, "一批中不能包含重复 IP", nil)
			return
		}
		seen[canonical] = struct{}{}
		if item.Success && item.DelayMS == 0 {
			item.DelayMS = 0.01
		}
		if item.CheckedAt.IsZero() || item.CheckedAt.After(time.Now().Add(2*time.Minute)) || item.CheckedAt.Before(time.Now().Add(-24*time.Hour)) {
			item.CheckedAt = time.Now().UTC()
		}
	}
	body, err := json.Marshal(input.Results)
	if err != nil {
		apiError(w, http.StatusBadRequest, "上报数据格式错误", nil)
		return
	}
	var saved int64
	err = a.db.QueryRow(r.Context(), `WITH received AS (
SELECT r.ip::inet AS ip,r.delay_ms,r.success,r.checked_at
FROM jsonb_to_recordset($2::jsonb) AS r(ip text,delay_ms double precision,success boolean,checked_at timestamptz)
), inserted AS (
INSERT INTO latency_probe_results(probe_id,ip,delay_ms,success,checked_at)
SELECT $1,p.ip,p.delay_ms,p.success,p.checked_at FROM received p
WHERE EXISTS(SELECT 1 FROM node_ip_pool pool WHERE pool.ip=p.ip AND pool.available)
AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=p.ip)
RETURNING 1
), seen AS (
UPDATE latency_probes SET last_seen=now() WHERE id=$1 RETURNING id
), pruned AS (
DELETE FROM latency_probe_results WHERE checked_at<now()-interval '30 days' AND probe_id=$1 RETURNING 1
)
SELECT count(*) FROM inserted`, id, string(body)).Scan(&saved)
	if err != nil {
		dbError(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]int64{"saved": saved})
}

