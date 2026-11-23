package ipguard

import (
	"context"
	"fmt"
	"time"
)

// Both successful and failed probes count as checked. Pool rows persist beyond
// task snapshots and record retention; unfinished addresses have no row.
func (a *app) checkedScanIPs(ctx context.Context, cidrs []string, protocol string) ([]string, error) {
	return a.checkedScanIPsSince(ctx, cidrs, protocol, nil)
}

// A shared cutoff lets full rechecks resume and deduplicate overlapping ranges.
func (a *app) checkedScanIPsSince(ctx context.Context, cidrs []string, protocol string, since *time.Time) ([]string, error) {
	query := `SELECT host(ip) FROM cf_ip_pool WHERE ip <<= ANY($1::cidr[])`
	args := []any{cidrs}
	if protocol != "" {
		// Quarantine controls admission, not whether an address was tested.
		// Count current-config failures too, or the automatic queue repeatedly
		// scans isolated addresses and never advances to the next network.
		// Historical unchecked successes still need certificate revalidation.
		if len(cidrs) == 1 {
			// Use explicit bounds for the common one-network automatic job. A
			// prepared generic plan cannot derive a btree range from <<= ANY,
			// and would otherwise scan the entire partial index.
			query = `SELECT host(p.ip) FROM node_ip_pool p
 WHERE p.ip >= network($1::cidr)::inet AND p.ip <= host(broadcast($1::cidr))::inet
 AND p.protocol=$2
 AND (NOT p.available OR p.certificate->>'status' IN ('passed','not_required'))
 AND EXISTS(SELECT 1 FROM nodes n WHERE n.enabled AND n.share_uri<>'' AND md5(n.share_uri)=p.config_hash)`
			args = []any{cidrs[0], protocol}
		} else {
			query = `SELECT host(p.ip) FROM node_ip_pool p
 WHERE p.ip <<= ANY($1::cidr[]) AND p.protocol=$2
 AND (NOT p.available OR p.certificate->>'status' IN ('passed','not_required'))
 AND EXISTS(SELECT 1 FROM nodes n WHERE n.enabled AND n.share_uri<>'' AND md5(n.share_uri)=p.config_hash)`
			args = []any{cidrs, protocol}
		}
	}
	if since != nil {
		args = append(args, *since)
		query += fmt.Sprintf(" AND checked_at >= $%d", len(args))
	}
	rows, err := a.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	checked := []string{}
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, err
		}
		checked = append(checked, ip)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	blocked, err := a.blacklistIPs(ctx, cidrs)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(checked)+len(blocked))
	for _, ip := range checked {
		seen[ip] = true
	}
	for _, ip := range blocked {
		if !seen[ip] {
			checked = append(checked, ip)
			seen[ip] = true
		}
	}
	return checked, nil
}
