package main

import (
	"context"
	"math/big"
	"net/http"
	"net/netip"

	"github.com/jackc/pgx/v5"
)

// Only a completed full round covering every address may disable a range.
func (a *app) disableEmptyNetworks(ctx context.Context, tx pgx.Tx, job scanJob) error {
	if job.Status != "completed" || job.Scope != "full" || job.Completed != job.Total {
		return nil
	}
	results := map[netip.Addr]bool{}
	for _, result := range job.Results {
		ip, err := netip.ParseAddr(result.IP)
		if err == nil {
			results[ip] = result.Status == "success"
		}
	}
	for _, cidr := range job.CIDRs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			continue
		}
		count, success := 0, false
		for ip, available := range results {
			if prefix.Contains(ip) {
				count++
				success = success || available
			}
		}
		expected := new(big.Int).Lsh(big.NewInt(1), uint(prefix.Addr().BitLen()-prefix.Bits()))
		if success || expected.Cmp(big.NewInt(int64(count))) != 0 {
			continue
		}
		_, err = tx.Exec(ctx, `INSERT INTO protocol_networks(protocol,network_id,cidr,checked_at)
   SELECT $1::varchar(32),id,cidr,$3::timestamptz FROM networks WHERE id=ANY($2::uuid[]) AND cidr=$4::cidr
   AND NOT EXISTS(SELECT 1 FROM node_ip_pool s WHERE s.protocol=$1::varchar(32) AND s.available AND s.ip <<= $4::cidr)
   ON CONFLICT(protocol,network_id) DO UPDATE SET cidr=excluded.cidr,checked_at=excluded.checked_at
   WHERE protocol_networks.checked_at<=excluded.checked_at`, job.Protocol, job.Input.NetworkIDs, job.FinishedAt, cidr)
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *app) restoreProtocolNetwork(w http.ResponseWriter, r *http.Request) {
	id, protocol := r.PathValue("id"), r.URL.Query().Get("protocol")
	if validate.Var(id, "uuid") != nil || len(protocol) > 32 || protocol == "" {
		apiError(w, 422, "网段或协议无效", nil)
		return
	}
	if _, err := a.db.Exec(r.Context(), "DELETE FROM protocol_networks WHERE network_id=$1 AND protocol=$2", id, protocol); err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

// Use current range settings, not an obsolete snapshot, for every patrol round.
func (a *app) protocolRanges(ctx context.Context, ids []string, protocol string) ([]string, error) {
	rows, err := a.db.Query(ctx, `SELECT w.cidr::text FROM networks w
 WHERE w.id=ANY($1::uuid[]) AND w.enabled AND NOT EXISTS (
 SELECT 1 FROM protocol_networks p WHERE p.network_id=w.id AND p.cidr=w.cidr AND p.protocol=$2)`, ids, protocol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cidrs := []string{}
	for rows.Next() {
		var cidr string
		if err := rows.Scan(&cidr); err != nil {
			return nil, err
		}
		cidrs = append(cidrs, cidr)
	}
	return cidrs, rows.Err()
}
