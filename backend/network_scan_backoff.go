package main

import (
	"context"
	"net/netip"
	"time"
)

func usesDailyNetworkBackoff(job scanJob) bool {
	return job.Protocol == connectivityProtocol && job.Input.IntervalMinutes == 1440
}

// A daily full scan is still daily for productive ranges. Ranges without any
// successful IPs move through 1, 3, 5, 7, then 15 day intervals.
func (a *app) dueDailyFullScanCIDRs(ctx context.Context, job scanJob, cidrs []string) ([]string, error) {
	if !usesDailyNetworkBackoff(job) || len(cidrs) == 0 {
		return cidrs, nil
	}
	rows, err := a.db.Query(ctx, `SELECT n.cidr::text FROM networks n
 LEFT JOIN network_scan_backoff b ON b.network_id=n.id AND b.protocol=$2 AND b.cidr=n.cidr
 WHERE n.id=ANY($1::uuid[]) AND n.enabled AND n.cidr=ANY($3::cidr[])
 AND (b.network_id IS NULL OR b.next_scan_at<=now())`, job.Input.NetworkIDs, job.Protocol, cidrs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	due := map[string]bool{}
	for rows.Next() {
		var cidr string
		if err = rows.Scan(&cidr); err != nil {
			return nil, err
		}
		due[cidr] = true
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	filtered := make([]string, 0, len(due))
	for _, cidr := range cidrs {
		if due[cidr] {
			filtered = append(filtered, cidr)
		}
	}
	return filtered, nil
}

func keepIPsInCIDRs(ips []netip.Addr, cidrs []string) []netip.Addr {
	prefixes := make([]netip.Prefix, 0, len(cidrs))
	for _, cidr := range cidrs {
		if prefix, err := netip.ParsePrefix(cidr); err == nil {
			prefixes = append(prefixes, prefix)
		}
	}
	filtered := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		for _, prefix := range prefixes {
			if prefix.Contains(ip) {
				filtered = append(filtered, ip)
				break
			}
		}
	}
	return filtered
}

func (a *app) updateDailyNetworkBackoff(ctx context.Context, job scanJob) error {
	if !usesDailyNetworkBackoff(job) || job.Scope != "full" || job.Status != "completed" || job.Completed != job.Total || len(job.CIDRs) == 0 {
		return nil
	}
	produced := make(map[string]bool, len(job.CIDRs))
	for _, cidr := range job.CIDRs {
		produced[cidr] = false
	}
	for _, result := range job.Results {
		if result.Status != "success" {
			continue
		}
		ip, err := netip.ParseAddr(result.IP)
		if err != nil {
			continue
		}
		for cidr := range produced {
			if prefix, err := netip.ParsePrefix(cidr); err == nil && prefix.Contains(ip) {
				produced[cidr] = true
			}
		}
	}
	cidrs := make([]string, 0, len(produced))
	hasOutput := make([]bool, 0, len(produced))
	for _, cidr := range job.CIDRs {
		if _, ok := produced[cidr]; !ok {
			continue
		}
		cidrs = append(cidrs, cidr)
		hasOutput = append(hasOutput, produced[cidr])
		delete(produced, cidr)
	}
	_, err := a.db.Exec(ctx, `WITH scanned AS (
  SELECT x.cidr,x.produced,n.id AS network_id,
   COALESCE(b.no_output_streak,0) AS previous_streak
  FROM unnest($2::cidr[],$3::boolean[]) AS x(cidr,produced)
  JOIN networks n ON n.id=ANY($1::uuid[]) AND n.cidr=x.cidr AND n.enabled
  LEFT JOIN network_scan_backoff b ON b.network_id=n.id AND b.protocol=$4 AND b.cidr=n.cidr
 )
 INSERT INTO network_scan_backoff(network_id,cidr,protocol,no_output_streak,interval_days,next_scan_at,updated_at)
 SELECT network_id,cidr,$4,
  CASE WHEN produced THEN 0 ELSE previous_streak+1 END,
  CASE WHEN produced THEN 1
   WHEN previous_streak<2 THEN 1
   WHEN previous_streak=2 THEN 3
   WHEN previous_streak=3 THEN 5
   WHEN previous_streak=4 THEN 7 ELSE 15 END,
  now()+make_interval(days => CASE WHEN produced THEN 1
   WHEN previous_streak<2 THEN 1
   WHEN previous_streak=2 THEN 3
   WHEN previous_streak=3 THEN 5
   WHEN previous_streak=4 THEN 7 ELSE 15 END),now()
 FROM scanned
 ON CONFLICT(network_id,protocol) DO UPDATE SET
  cidr=excluded.cidr,no_output_streak=excluded.no_output_streak,
  interval_days=excluded.interval_days,next_scan_at=excluded.next_scan_at,updated_at=excluded.updated_at`,
		job.Input.NetworkIDs, cidrs, hasOutput, job.Protocol)
	return err
}

func nextDailyScanAt() time.Time { return time.Now().UTC().Add(24 * time.Hour) }
