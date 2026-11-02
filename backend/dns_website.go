package main

import (
	"context"
	"encoding/json"
	"time"
)

const websiteAdmissionDelayMS = 70
const websiteStablePeriod = 15 * time.Minute
const websiteReentryCooldown = 30 * time.Minute

// Keep the 80ms removal limit. A stricter admission limit and sustained
// evidence prevent an IP hovering near that limit from immediately returning.
func dnsWebsiteAdmissionOK(item dnsCandidate) bool {
	if item.Pool != "cf" || (item.State != "healthy" && item.State != "pending") {
		return false
	}
	if item.Country == "HK" {
		return true
	}
	return (item.Country == "JP" || item.Country == "SG") && item.Delay >= 0 && item.Delay <= websiteAdmissionDelayMS
}

func (a *app) dnsWebsiteHistory(ctx context.Context, c dnsConfig) ([]dnsReport, error) {
	lookback := max(45*time.Minute, 4*time.Duration(c.Interval)*time.Second)
	rows, err := a.db.Query(ctx, `SELECT jsonb_build_object(
 'mode',report->'mode','config',report->'config','started_at',report->'started_at',
 'error',report->'error','warning',report->'warning',
 'records',COALESCE((SELECT jsonb_agg(r) FROM jsonb_array_elements(report->'records') r WHERE r->>'name'=$2),'[]'::jsonb),
 'candidates',COALESCE((SELECT jsonb_agg(ip) FROM jsonb_array_elements(report->'candidates') ip WHERE ip->>'pool'='cf'),'[]'::jsonb))
 FROM dns_sync_runs WHERE started_at >= now()-make_interval(secs=>$3)
 AND report->>'domain'=$1 AND report->>'mode'='sync' ORDER BY started_at DESC`, c.Domain, "url."+c.Domain, int(lookback.Seconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := []dnsReport{}
	for rows.Next() {
		var raw []byte
		var report dnsReport
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &report); err != nil {
			return nil, err
		}
		history = append(history, report)
	}
	return history, rows.Err()
}

func dnsWebsiteStable(c dnsConfig, item dnsCandidate, history []dnsReport, now time.Time) bool {
	if !dnsWebsiteAdmissionOK(item) {
		return false
	}
	for _, report := range history {
		if report.StartedAt.After(now) || now.Sub(report.StartedAt) >= websiteReentryCooldown {
			continue
		}
		for _, record := range report.Records {
			if record.Name != "url."+c.Domain || !record.Applied || (record.Action != "update" && record.Action != "delete") {
				continue
			}
			for _, removed := range dnsIPDifference(record.Current, record.Desired) {
				if removed == item.IP {
					return false
				}
			}
		}
	}
	stablePeriod := max(websiteStablePeriod, 2*time.Duration(c.Interval)*time.Second)
	maxGap := 2*time.Duration(c.Interval)*time.Second + time.Minute
	previous := now
	for _, report := range history {
		if report.StartedAt.After(now) {
			continue
		}
		if report.Mode != "sync" || report.Error != "" || report.Warning != "" || report.Config.Domain != c.Domain || !report.Config.WebsiteEnabled || report.Config.ProbeID != c.ProbeID || report.Config.Window != c.Window || report.Config.MinSuccess != c.MinSuccess || previous.Sub(report.StartedAt) > maxGap {
			return false
		}
		found := false
		for _, old := range report.Candidates {
			if old.IP == item.IP && old.Country == item.Country && dnsWebsiteAdmissionOK(old) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
		if now.Sub(report.StartedAt) >= stablePeriod {
			return true
		}
		previous = report.StartedAt
	}
	return false
}

// Only delay additions. Published IPs still follow the existing health,
// blacklist, pool and <=80ms eligibility checks without a removal cooldown.
func dnsStabilizeWebsite(c dnsConfig, candidates []dnsCandidate, remote []dnsRemoteRecord, history []dnsReport, now time.Time) {
	published := map[string]bool{}
	for _, record := range remote {
		if record.Name == "url."+c.Domain && record.Line == "default_view" && (record.Type == "A" || record.Type == "AAAA") {
			for _, ip := range record.Values {
				published[ip] = true
			}
		}
	}
	for i := range candidates {
		item := &candidates[i]
		if item.Pool != "cf" || item.State != "healthy" || published[item.IP] {
			continue
		}
		if !dnsWebsiteStable(c, *item, history, now) {
			item.State = "pending"
			item.Reason = "等待稳定入选：港日新连续达标至少 15 分钟；日本/新加坡加入需 ≤70 ms，移除后冷却 30 分钟"
		}
	}
}
