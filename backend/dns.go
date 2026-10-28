package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const dnsLockID int64 = 74281934

type dnsReport struct {
	Mode       string          `json:"mode"`
	Domain     string          `json:"domain"`
	Config     dnsConfig       `json:"config"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	Error      string          `json:"error,omitempty"`
	Warning    string          `json:"warning,omitempty"`
	Records    []dnsPlanRecord `json:"records"`
	Candidates []dnsCandidate  `json:"candidates"`
}

func (a *app) loadDNSConfig(ctx context.Context) (dnsConfig, error) {
	var raw []byte
	var c dnsConfig
	err := a.db.QueryRow(ctx, "SELECT config FROM dns_settings WHERE id").Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	return c, err
}

// This session lock serializes cloud runs, policy edits and queue requests,
// including across multiple API processes. Never return a locked pool session.
func (a *app) dnsLock(ctx context.Context) (*pgxpool.Conn, error) {
	conn, err := a.db.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var locked bool
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", dnsLockID).Scan(&locked); err != nil {
		conn.Release()
		return nil, err
	}
	if !locked {
		conn.Release()
		return nil, fmt.Errorf("DNS 任务正在执行，请等待完成")
	}
	return conn, nil
}
func dnsUnlock(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", dnsLockID); err != nil {
		_ = conn.Conn().Close(ctx)
	}
	conn.Release()
}

func (a *app) getDNS(w http.ResponseWriter, r *http.Request) {
	c, err := a.loadDNSConfig(r.Context())
	if err != nil {
		dbError(w, err)
		return
	}
	var running bool
	var requested string
	var next time.Time
	var report []byte
	err = a.db.QueryRow(r.Context(), "SELECT running,requested,next_run_at,report FROM dns_settings WHERE id").Scan(&running, &requested, &next, &report)
	if err != nil {
		dbError(w, err)
		return
	}
	var history []byte
	err = a.db.QueryRow(r.Context(), `SELECT COALESCE(json_agg(h),'[]') FROM (SELECT id,started_at,report FROM dns_sync_runs ORDER BY id DESC LIMIT 10) h`).Scan(&history)
	if err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, map[string]any{"config": c, "credentials_ready": dnsCredentialsReady(), "running": running, "requested": requested, "next_run_at": next, "report": json.RawMessage(report), "history": json.RawMessage(history)})
}
func (a *app) saveDNS(w http.ResponseWriter, r *http.Request) {
	var c dnsConfig
	if !decode(w, r, &c) {
		return
	}
	if err := c.validate(); err != nil {
		apiError(w, 422, err.Error(), nil)
		return
	}
	if c.ProbeID != "" {
		var exists bool
		if err := a.db.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM latency_probes WHERE id=$1 AND enabled)", c.ProbeID).Scan(&exists); err != nil {
			dbError(w, err)
			return
		}
		if !exists {
			apiError(w, 422, "所选探针不存在或已停用", nil)
			return
		}
	}
	conn, err := a.dnsLock(r.Context())
	if err != nil {
		apiError(w, 409, err.Error(), nil)
		return
	}
	defer dnsUnlock(conn)
	raw, _ := json.Marshal(c)
	_, err = conn.Exec(r.Context(), "UPDATE dns_settings SET config=$1,requested='',next_run_at=now()+make_interval(secs=>$2) WHERE id", string(raw), c.Interval)
	if err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
func (a *app) requestDNS(w http.ResponseWriter, r *http.Request) {
	mode := r.PathValue("mode")
	if mode != "preview" && mode != "sync" {
		apiError(w, 404, "未知 DNS 操作", nil)
		return
	}
	if !dnsCredentialsReady() {
		apiError(w, 422, "请先在服务器配置华为云 AK/SK 并重启 Go API", nil)
		return
	}
	conn, err := a.dnsLock(r.Context())
	if err != nil {
		apiError(w, 409, err.Error(), nil)
		return
	}
	defer dnsUnlock(conn)
	tag, err := conn.Exec(r.Context(), "UPDATE dns_settings SET requested=$1 WHERE id AND requested=''", mode)
	if err != nil {
		dbError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		apiError(w, 409, "DNS 任务已排队，请等待完成", nil)
		return
	}
	respond(w, 202, map[string]bool{"ok": true})
}

func (a *app) maintainDNS(ctx context.Context) {
	timer := time.NewTicker(5 * time.Second)
	defer timer.Stop()
	for {
		a.runDNSOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
func (a *app) runDNSOnce(ctx context.Context) {
	conn, err := a.dnsLock(ctx)
	if err != nil {
		return
	}
	defer dnsUnlock(conn)
	var requested string
	var due bool
	var interrupted bool
	err = conn.QueryRow(ctx, `SELECT requested,(config->>'enabled')::boolean AND next_run_at<=now(),running FROM dns_settings WHERE id`).Scan(&requested, &due, &interrupted)
	if err != nil {
		log.Printf("DNS 配置未就绪，请执行数据库初始化: %v", err)
		return
	}
	if interrupted {
		_, _ = conn.Exec(ctx, `UPDATE dns_settings SET running=false,report=jsonb_set(report,'{error}','"上次任务因服务中断而停止，下轮重新核对云端解析"'::jsonb) WHERE id`)
	}
	if requested == "" && !due {
		return
	}
	c, err := a.loadDNSConfig(ctx)
	if err != nil {
		return
	}
	if requested == "" {
		requested = "sync"
	}
	if _, err = conn.Exec(ctx, "UPDATE dns_settings SET running=true,requested='' WHERE id"); err != nil {
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	report := dnsReport{Mode: requested, Domain: c.Domain, Config: c, StartedAt: time.Now().UTC(), Records: []dnsPlanRecord{}, Candidates: []dnsCandidate{}}
	if err = c.validate(); err == nil {
		err = a.executeDNS(runCtx, c, &report, nil)
	}
	if err != nil {
		report.Error = err.Error()
	}
	finished := time.Now().UTC()
	report.FinishedAt = &finished
	raw, _ := json.Marshal(report)
	// Persist the outcome even when graceful shutdown canceled the run.
	finishCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	tx, err := conn.Begin(finishCtx)
	if err != nil {
		return
	}
	defer tx.Rollback(finishCtx)
	if _, err = tx.Exec(finishCtx, "UPDATE dns_settings SET running=false,report=$1,next_run_at=now()+make_interval(secs=>$2) WHERE id", string(raw), c.Interval); err != nil {
		return
	}
	if _, err = tx.Exec(finishCtx, "INSERT INTO dns_sync_runs(started_at,report) VALUES($1,$2)", report.StartedAt, string(raw)); err != nil {
		return
	}
	_, _ = tx.Exec(finishCtx, "DELETE FROM dns_sync_runs WHERE id NOT IN (SELECT id FROM dns_sync_runs ORDER BY id DESC LIMIT 100)")
	if err = tx.Commit(finishCtx); err != nil {
		log.Printf("保存 DNS 任务失败: %v", err)
	}
}

func (a *app) dnsCandidates(ctx context.Context, c dnsConfig) ([]dnsCandidate, bool, error) {
	// Require an online probe with recent results even when the available pool
	// is empty; a monitoring outage must not delete every country's record.
	var monitorReady bool
	err := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM latency_probes p WHERE `+probeOnlineSQL+`
 AND ($1='' OR p.id::text=$1) AND EXISTS(SELECT 1 FROM latency_probe_results r
 WHERE r.probe_id=p.id AND r.mode=p.mode AND r.tcp_port=CASE WHEN p.mode='tcp' THEN p.tcp_port ELSE 0 END
 AND r.checked_at>now()-make_interval(secs=>LEAST(300,`+probeSampleWindowSQL+`*2))))`, c.ProbeID).Scan(&monitorReady)
	if err != nil {
		return nil, false, err
	}
	rows, err := a.db.Query(ctx, `SELECT DISTINCT host(ip) FROM current_node_ip_pool s WHERE available
 AND ($1='' OR protocol=$1) AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=s.ip) ORDER BY host(ip)`, c.Protocol)
	if err != nil {
		return nil, false, err
	}
	items := []dnsCandidate{}
	index := map[string]int{}
	for rows.Next() {
		var ip string
		if err = rows.Scan(&ip); err != nil {
			rows.Close()
			return nil, false, err
		}
		index[ip] = len(items)
		items = append(items, dnsCandidate{IP: ip, State: "unknown", Reason: "没有在线探针的新鲜完整采样"})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	// Only selected, online probes and samples matching the current mode count.
	rows, err = a.db.Query(ctx, `SELECT host(t.ip),LEAST(300,`+probeSampleWindowSQL+`*2),COALESCE(samples.data,'[]'::jsonb)
 FROM latency_probes p CROSS JOIN (SELECT DISTINCT ip FROM current_node_ip_pool s WHERE available
 AND ($1='' OR protocol=$1) AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=s.ip)) t
 LEFT JOIN LATERAL (SELECT jsonb_agg(jsonb_build_object('At',checked_at,'Success',success,'Delay',delay_ms) ORDER BY checked_at,id) AS data
 FROM latency_probe_results WHERE probe_id=p.id AND ip=t.ip AND mode=p.mode AND tcp_port=CASE WHEN p.mode='tcp' THEN p.tcp_port ELSE 0 END
 AND checked_at>=now()-make_interval(mins=>$3)) samples ON true
 WHERE `+probeOnlineSQL+` AND ($2='' OR p.id::text=$2)`, c.Protocol, c.ProbeID, c.Window)
	if err != nil {
		return nil, false, err
	}
	now := time.Now()
	states := map[string][]string{}
	for rows.Next() {
		var ip string
		var fresh int
		var data []byte
		if err = rows.Scan(&ip, &fresh, &data); err != nil {
			break
		}
		var samples []dnsSample
		if err = json.Unmarshal(data, &samples); err != nil {
			break
		}
		state, delay := dnsProbeHealth(samples, now, time.Duration(fresh)*time.Second, c)
		states[ip] = append(states[ip], state)
		i, ok := index[ip]
		if !ok {
			continue
		}
		if state == "healthy" && (items[i].State != "healthy" || delay < items[i].Delay) {
			items[i].State = "healthy"
			items[i].Delay = delay
			items[i].Reason = "近期采样成功率达标且最新采样成功"
			if c.MinSuccess == 0 {
				items[i].Reason = "近期有成功采样，未连续失败满 60 秒"
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	for i := range items {
		if items[i].State == "healthy" {
			continue
		}
		s := states[items[i].IP]
		if len(s) > 0 {
			failed := true
			for _, v := range s {
				if v != "failed" {
					failed = false
				}
			}
			if failed {
				items[i].State = "failed"
				items[i].Reason = "连续失败至少 60 秒，或观察窗口成功率不足"
				if c.MinSuccess == 0 {
					items[i].Reason = "连续失败至少 60 秒，或观察窗口内没有成功采样"
				}
			}
		}
	}
	return items, !monitorReady, nil
}

func (a *app) dnsLocation(ctx context.Context, ip string) (string, error) {
	var country string
	err := a.db.QueryRow(ctx, "SELECT country FROM dns_ip_locations WHERE ip=$1::inet AND checked_at>now()-interval '24 hours'", ip).Scan(&country)
	if err == nil && dnsCountry(country) != "" {
		return country, nil
	}
	if a.redis != nil {
		raw, err := a.redis.Get(ctx, "ipfilter:ip-location:v2:"+ip).Bytes()
		var location ipLocation
		if err == nil && json.Unmarshal(raw, &location) == nil && dnsCountry(location.Country) != "" {
			country = location.Country
		}
	}
	if country == "" {
		ipAddr, err := netip.ParseAddr(ip)
		if err != nil || !publicMetadataPrefix(netip.PrefixFrom(ipAddr, ipAddr.BitLen())) {
			return "", fmt.Errorf("非公网地址")
		}
		lookupCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		defer cancel()
		location, err := lookupIPLocation(lookupCtx, http.DefaultClient, "https://api.123169.xyz", ip)
		if err != nil {
			return "", err
		}
		country = location.Country
	}
	if dnsCountry(country) == "" {
		return "", fmt.Errorf("归属国家或地区未知")
	}
	_, err = a.db.Exec(ctx, `INSERT INTO dns_ip_locations(ip,country) VALUES($1::inet,$2) ON CONFLICT(ip) DO UPDATE SET country=excluded.country,checked_at=now()`, ip, country)
	return country, err
}

func (a *app) executeDNS(ctx context.Context, c dnsConfig, report *dnsReport, provider dnsProvider) error {
	var err error
	if provider == nil {
		provider, err = newHuaweiDNS()
		if err != nil {
			return err
		}
	}
	zone, err := provider.Zone(ctx, c.Domain)
	if err != nil {
		return err
	}
	remote, err := provider.Records(ctx, zone)
	if err != nil {
		return err
	}
	candidates, uncertain, err := a.dnsCandidates(ctx, c)
	if err != nil {
		return err
	}
	// Bound external geolocation concurrency. Resolve unknown candidates too,
	// so their existing country records are held rather than accidentally erased.
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if candidates[i].State == "failed" {
					continue
				}
				country, err := a.dnsLocation(ctx, candidates[i].IP)
				if err != nil {
					mu.Lock()
					uncertain = true
					mu.Unlock()
					candidates[i].State = "unknown"
					candidates[i].Reason = "归属地查询失败，暂不发布"
				} else {
					candidates[i].Country = country
				}
			}
		}()
	}
	for i := range candidates {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	managed := map[string]bool{}
	rows, err := a.db.Query(ctx, "SELECT name,type FROM dns_managed_records WHERE domain=$1 AND zone_id=$2", c.Domain, zone)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name, kind string
		if err = rows.Scan(&name, &kind); err != nil {
			rows.Close()
			return err
		}
		managed[dnsRecordKey(name, kind)] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	report.Candidates = candidates
	report.Records = dnsBuildPlan(c, candidates, remote, managed, uncertain)
	if uncertain {
		report.Warning = "探针整体不可用或归属地查询不完整，本轮保留云端解析"
	}
	if report.Mode == "preview" {
		return nil
	}
	for i := range report.Records {
		r := &report.Records[i]
		if r.Action == "hold" {
			continue
		}
		// Check the target again after potentially slow probe/geolocation work.
		// Huawei DNS has no conditional update; this narrows concurrent-edit races.
		latest, readErr := provider.Records(ctx, zone)
		if readErr != nil {
			return readErr
		}
		if !dnsSameRemote(remote, latest, r.Name, r.Type) {
			r.Action = "hold"
			r.Reason = "云端解析在检查期间发生变化，下轮重新核对"
			continue
		}
		if r.Action != "unchanged" {
			if err = provider.Apply(ctx, zone, *r, c.TTL); err != nil {
				r.Reason = "云端写入失败"
				return fmt.Errorf("同步 %s %s 失败：%w", r.Name, r.Type, err)
			}
		}
		r.Applied = true
		if r.Action == "delete" {
			_, err = a.db.Exec(ctx, "DELETE FROM dns_managed_records WHERE domain=$1 AND name=$2 AND type=$3 AND zone_id=$4", c.Domain, r.Name, r.Type, zone)
		} else {
			_, err = a.db.Exec(ctx, "INSERT INTO dns_managed_records(domain,name,type,zone_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", c.Domain, r.Name, r.Type, zone)
		}
		if err != nil {
			return err
		}
	}
	sort.Slice(report.Candidates, func(i, j int) bool { return report.Candidates[i].IP < report.Candidates[j].IP })
	return nil
}
