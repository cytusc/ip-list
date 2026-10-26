package main

import "context"

// Both successful and failed probes count as checked. Pool rows persist beyond
// task snapshots and record retention; unfinished addresses have no row.
func (a *app) checkedScanIPs(ctx context.Context, cidrs []string, protocol string) ([]string, error) {
	query := `SELECT host(ip) FROM cf_ip_pool WHERE ip <<= ANY($1::cidr[])`
	args := []any{cidrs}
	if protocol != "" {
		query = `SELECT host(ip) FROM node_ip_pool WHERE ip <<= ANY($1::cidr[]) AND protocol=$2`
		args = append(args, protocol)
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
