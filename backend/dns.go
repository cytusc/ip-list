package main

import (
	"context"
	"encoding/json"
	"errors"
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
const dnsMonitorUnavailable = "依据探针离线或缺少新鲜采样，暂停全部 DNS 同步并保留原解析"

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
	// Upgrade legacy website policies using their existing scan target. The
	// effective URL is returned to the UI and stored in subsequent reports.
	if err == nil && c.WebsiteEnabled && c.WebsiteURL == "" {
		err = a.db.QueryRow(ctx, "SELECT COALESCE((SELECT url FROM cf_targets ORDER BY created_at LIMIT 1),'')").Scan(&c.WebsiteURL)
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
	reportColumn := "report"
	if r.URL.Query().Get("summary") == "1" {
		reportColumn = `(report-'records'-'candidates') || jsonb_build_object('candidate_count',jsonb_array_length(COALESCE(report->'candidates','[]'::jsonb)))`
	}
	err = a.db.QueryRow(r.Context(), "SELECT running,requested,next_run_at,"+reportColumn+" FROM dns_settings WHERE id").Scan(&running, &requested, &next, &report)
	if err != nil {
		dbError(w, err)
		return
	}
	history := []byte("[]")
	if r.URL.Query().Get("summary") != "1" {
		err = a.db.QueryRow(r.Context(), `SELECT COALESCE(json_agg(h),'[]') FROM (SELECT id,started_at,report FROM dns_sync_runs ORDER BY id DESC LIMIT 10) h`).Scan(&history)
	}
	if err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, map[string]any{"config": c, "credentials_ready": dnsCredentialsReady(), "running": running, "requested": requested, "next_run_at": next, "report": json.RawMessage(report), "history": json.RawMessage(history)})
}

// Return only the visible rows of a report, never the full reports of every
// historical run. History needs just the small summary fields.
func (a *app) listDNSItems(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	var countSQL, dataSQL string
	switch kind {
	case "history":
		countSQL = "SELECT count(*) FROM dns_sync_runs"
		dataSQL = `SELECT COALESCE(json_agg(h),'[]') FROM (SELECT id,started_at,report-'records'-'candidates' AS report FROM dns_sync_runs ORDER BY id DESC LIMIT $1 OFFSET $2) h`
	case "records", "candidates":
		source := ` FROM dns_settings CROSS JOIN LATERAL jsonb_array_elements(COALESCE(report->'` + kind + `','[]'::jsonb)) WITH ORDINALITY AS e(value,ord) WHERE id`
		countSQL = `SELECT count(*)` + source
		dataSQL = `SELECT COALESCE(jsonb_agg(value ORDER BY ord),'[]'::jsonb) FROM (SELECT value,ord` + source + ` ORDER BY ord LIMIT $1 OFFSET $2) selected`
	default:
		apiError(w, 404, "列表不存在", nil)
		return
	}
	p, ok := a.listPage(w, r, 50, countSQL)
	if !ok {
		return
	}
	var data json.RawMessage
	if err := a.db.QueryRow(r.Context(), dataSQL, p.Size, p.Offset()).Scan(&data); err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, data)
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
	notifyDNSReport(&report)
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
	return a.dnsPoolCandidates(ctx, c, false)
}

func (a *app) dnsMonitorReady(ctx context.Context, c dnsConfig) (bool, error) {
	var monitorReady bool
	err := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM latency_probes p WHERE `+probeOnlineSQL+`
 AND ($1='' OR p.id::text=$1) AND EXISTS(SELECT 1 FROM latency_probe_results r
 WHERE r.probe_id=p.id AND r.mode=p.mode AND r.tcp_port=CASE WHEN p.mode='tcp' THEN p.tcp_port ELSE 0 END
 AND r.checked_at>now()-make_interval(secs=>LEAST(300,`+probeSampleWindowSQL+`*2))))`, c.ProbeID).Scan(&monitorReady)
	return monitorReady, err
}

func (a *app) dnsPoolCandidates(ctx context.Context, c dnsConfig, website bool) ([]dnsCandidate, bool, error) {
	// Expired proofs must be rechecked, not silently treated as absent/failed.
	poolSQL := `SELECT p.ip,n.protocol,p.available FROM node_ip_evidence p CROSS JOIN (
 SELECT DISTINCT split_part(share_uri,'://',1)||'/'||COALESCE(substring(share_uri FROM '[?&]type=([^&#]+)'),'tcp') AS protocol
 FROM nodes WHERE enabled AND share_uri ~ '^(vless|trojan)://'
 ) n WHERE p.protocol='grpc'`
	if website {
		poolSQL = "SELECT ip,''::text AS protocol,available FROM cf_ip_pool"
		c.Protocol = ""
	}

	// An empty pool must not turn a monitoring outage into record deletions.
	monitorReady, err := a.dnsMonitorReady(ctx, c)
	if err != nil {
		return nil, false, err
	}
	rows, err := a.db.Query(ctx, `SELECT DISTINCT host(s.ip),COALESCE(info.provider,'') FROM (`+poolSQL+`) s `+networkInfoJoin+` WHERE s.available
 AND ($1='' OR s.protocol=$1) AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=s.ip) ORDER BY host(s.ip)`, c.Protocol)
	if err != nil {
		return nil, false, err
	}
	items := []dnsCandidate{}
	index := map[string]int{}
	for rows.Next() {
		var ip, provider string
		if err = rows.Scan(&ip, &provider); err != nil {
			rows.Close()
			return nil, false, err
		}
		index[ip] = len(items)
		items = append(items, dnsCandidate{IP: ip, Provider: provider, State: "unknown", Reason: "没有在线探针的新鲜完整采样"})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	// Only selected, online probes and samples matching the current mode count.
	// Materialize fresh probe controls once. The previous correlated anti-join
	// searched all retained history again for every candidate and successful row.
	rows, err = a.db.Query(ctx, `WITH probes AS MATERIALIZED (
 SELECT p.*,LEAST(300,`+probeSampleWindowSQL+`*2) AS fresh FROM latency_probes p
 WHERE `+probeOnlineSQL+` AND ($2='' OR p.id::text=$2)
 ), controls AS MATERIALIZED (
 SELECT p.id AS probe_id,r.ip,r.success FROM probes p
 CROSS JOIN LATERAL (SELECT DISTINCT ON (ip) ip,success
 FROM latency_probe_results WHERE probe_id=p.id AND mode=p.mode
 AND tcp_port=CASE WHEN p.mode='tcp' THEN p.tcp_port ELSE 0 END
 AND checked_at>now()-make_interval(secs=>p.fresh)
 ORDER BY ip,checked_at DESC,id DESC) r
 )
 SELECT host(t.ip),p.fresh,COALESCE(samples.data,'[]'::jsonb),
 EXISTS(SELECT 1 FROM controls ok WHERE ok.probe_id=p.id AND ok.ip<>t.ip AND ok.success)
 FROM probes p CROSS JOIN (SELECT DISTINCT ip FROM (`+poolSQL+`) s WHERE available
 AND ($1='' OR protocol=$1) AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=s.ip)) t
 LEFT JOIN LATERAL (SELECT jsonb_agg(jsonb_build_object('At',checked_at,'Success',success,'Delay',delay_ms) ORDER BY checked_at,id) AS data
 FROM latency_probe_results WHERE probe_id=p.id AND ip=t.ip AND mode=p.mode AND tcp_port=CASE WHEN p.mode='tcp' THEN p.tcp_port ELSE 0 END
 AND checked_at>=now()-make_interval(mins=>$3)) samples ON true`, c.Protocol, c.ProbeID, max(c.Window, 10))
	if err != nil {
		return nil, false, err
	}
	now := time.Now()
	states := map[string][]string{}
	for rows.Next() {
		var ip string
		var fresh int
		var data []byte
		var otherTargetOK bool
		if err = rows.Scan(&ip, &fresh, &data, &otherTargetOK); err != nil {
			break
		}
		var samples []dnsSample
		if err = json.Unmarshal(data, &samples); err != nil {
			break
		}
		state, delay := dnsProbeHealth(samples, now, time.Duration(fresh)*time.Second, c)
		uncorroborated := (state == "failed" || state == "pending") && !otherTargetOK
		state = dnsCorroborateFailure(state, otherTargetOK)
		states[ip] = append(states[ip], state)
		i, ok := index[ip]
		if !ok {
			continue
		}
		if uncorroborated && items[i].State != "healthy" {
			items[i].Reason = "探针近期没有其他目标成功，无法区分探针网络异常与 IP 故障"
		}
		if state == "healthy" && (items[i].State != "healthy" || delay < items[i].Delay) {
			items[i].State = "healthy"
			items[i].Delay = delay
			items[i].Reason = "平均延迟按成功样本 P95 剔除峰值后计算"
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
		if website {
			items[i].Pool = "cf"
		}
		if items[i].State == "healthy" {
			continue
		}
		s := states[items[i].IP]
		if len(s) > 0 {
			failed := true
			pending := true
			for _, v := range s {
				if v != "failed" && v != "pending" {
					pending = false
				}
				if v != "failed" {
					failed = false
				}
			}
			if failed {
				items[i].State = "failed"
				items[i].Reason = "连续失败至少 5 分钟"
			} else if pending {
				items[i].State, items[i].Reason = "pending", "观察窗口成功率低于策略门槛"
			}
		}
	}
	return items, !monitorReady, nil
}

func (a *app) dnsLocation(ctx context.Context, ip string) (string, error) {
	ipAddr, err := netip.ParseAddr(ip)
	if err != nil || !publicMetadataPrefix(netip.PrefixFrom(ipAddr, ipAddr.BitLen())) {
		return "", fmt.Errorf("非公网地址")
	}
	location, err := a.resolveIPLocation(ctx, ip)
	if err != nil {
		return "", err
	}
	if location.Status == "conflict" || location.Status == "insufficient" {
		return "", errLocationConflict
	}
	if dnsCountry(location.Country) == "" {
		return "", fmt.Errorf("归属国家或地区未知")
	}
	return location.Country, nil
}

func (a *app) executeDNS(ctx context.Context, c dnsConfig, report *dnsReport, provider dnsProvider) error {
	return a.executeDNSWithNodeCheck(ctx, c, report, provider, a.dnsCheckNodes)
}

func (a *app) executeDNSWithNodeCheck(ctx context.Context, c dnsConfig, report *dnsReport, provider dnsProvider, checkNodes func(context.Context, []dnsCandidate) error) error {
	report.Warning = ""
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
		return fmt.Errorf("读取节点探针采样失败: %w", err)
	}
	report.Candidates = candidates
	if c.WebsiteEnabled {
		website, websiteUncertain, websiteErr := a.dnsPoolCandidates(ctx, c, true)
		if websiteErr != nil {
			return fmt.Errorf("读取网站探针采样失败: %w", websiteErr)
		}
		candidates = append(candidates, website...)
		report.Candidates = candidates
		uncertain = uncertain || websiteUncertain
	}
	if c.WebsiteEnabled {
		if err := a.dnsCheckWebsiteHealth(ctx, c, candidates); err != nil {
			return fmt.Errorf("DNS 发布前网站复查失败: %w", err)
		}
	}
	if err := checkNodes(ctx, candidates); err != nil {
		return fmt.Errorf("DNS 发布前节点复查失败: %w", err)
	}
	if err := a.filterDNSAvailableNodes(ctx, candidates); err != nil {
		return fmt.Errorf("读取可用 IP 池失败: %w", err)
	}
	// Resolve each IP once even when it belongs to both pools. A lookup failure
	// affects this IP only: retain it if published, and block new grouping.
	geoCtx, stopGeo := context.WithTimeout(ctx, 60*time.Second)
	defer stopGeo()
	byIP := map[string][]int{}
	for i := range candidates {
		if candidates[i].State != "failed" && candidates[i].State != "pending" {
			byIP[candidates[i].IP] = append(byIP[candidates[i].IP], i)
		}
	}
	jobs := make(chan string, len(byIP))
	for ip := range byIP {
		jobs <- ip
	}
	close(jobs)
	var wg sync.WaitGroup
	var mu sync.Mutex
	locationIncomplete := false
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ip := range jobs {
				country, err := a.dnsLocation(geoCtx, ip)
				for _, i := range byIP[ip] {
					if errors.Is(err, errLocationConflict) {
						candidates[i].State = "unknown"
						candidates[i].Reason = errLocationConflict.Error()
					} else if err != nil {
						mu.Lock()
						locationIncomplete = true
						mu.Unlock()
						candidates[i].State = "unknown"
						candidates[i].Reason = "归属地查询失败：保留该 IP 已有解析，暂停新增分组"
					} else {
						candidates[i].Country = country
					}
				}
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := a.filterDNSSlowIPs(ctx, candidates); err != nil {
		return fmt.Errorf("读取下载测速结果失败: %w", err)
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
	dnsSelectLatency(candidates)
	report.Candidates = candidates
	report.Records = dnsBuildPlan(c, candidates, remote, managed, uncertain)
	// A common probe/route failure must not erase the last published records.
	hasDesired, hasDelete := false, false
	for _, r := range report.Records {
		hasDesired = hasDesired || len(r.Desired) > 0
		hasDelete = hasDelete || r.Action == "delete"
	}
	if hasDelete && !hasDesired {
		report.Warning = "全部解析均将被删除，疑似探针或线路整体异常：保留现有解析，等待恢复或人工核查"
		for i := range report.Records {
			r := &report.Records[i]
			if r.Action == "delete" {
				r.Action = "hold"
				r.Desired = append([]string{}, r.Current...)
				r.Reason = report.Warning
			}
		}
	}
	if uncertain {
		report.Warning = dnsMonitorUnavailable
	} else if locationIncomplete {
		report.Warning = "部分 IP 定位查询不完整：仅保留这些 IP 的已有解析，不影响其他 IP 更新或失效清理"
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
		ready, readyErr := a.dnsMonitorReady(ctx, c)
		if readyErr != nil {
			return fmt.Errorf("复查依据探针状态失败: %w", readyErr)
		}
		if !ready {
			r.Action = "hold"
			r.Desired = append([]string{}, r.Current...)
			r.Reason = dnsMonitorUnavailable
			report.Warning = dnsMonitorUnavailable
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

// DNS may narrow the available pool, but cannot publish a new node IP outside it.
func (a *app) filterDNSAvailableNodes(ctx context.Context, items []dnsCandidate) error {
	rows, err := a.db.Query(ctx, availableIPCandidatesSQL+` SELECT host(ip) FROM candidates`, "", "", "", "")
	if err != nil {
		return err
	}
	defer rows.Close()
	available := map[string]bool{}
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return err
		}
		available[ip] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range items {
		if items[i].Pool != "cf" && items[i].State == "healthy" && !available[items[i].IP] {
			items[i].State, items[i].Reason = "unknown", "尚未进入可用 IP 池，暂停 DNS 入选"
		}
	}
	return nil
}
