package main

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

type blacklistedIP struct {
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
}

func canonicalIP(value string) (string, bool) {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || ip.Zone() != "" {
		return "", false
	}
	return ip.Unmap().String(), true
}

func (a *app) listBlacklist(w http.ResponseWriter, r *http.Request) {
	search := r.URL.Query().Get("search")
	where := " FROM ip_blacklist WHERE ($1='' OR position($1 in host(ip))>0)"
	p, ok := a.listPage(w, r, 50, "SELECT count(*)"+where, search)
	if !ok {
		return
	}
	rows, err := a.db.Query(r.Context(), "SELECT host(ip),created_at"+where+" ORDER BY ip LIMIT $2 OFFSET $3", search, p.Size, p.Offset())
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	items := []blacklistedIP{}
	for rows.Next() {
		var item blacklistedIP
		if err = rows.Scan(&item.IP, &item.CreatedAt); err != nil {
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

func (a *app) addBlacklist(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IPs []string `json:"ips"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.IPs) == 0 || len(input.IPs) > 500 {
		apiError(w, http.StatusUnprocessableEntity, "请输入 1–500 个 IP", nil)
		return
	}
	unique := map[string]bool{}
	ips := make([]string, 0, len(input.IPs))
	duplicates := 0
	for _, value := range input.IPs {
		ip, ok := canonicalIP(value)
		if !ok {
			apiError(w, http.StatusUnprocessableEntity, "黑名单只接受单个 IPv4 或 IPv6 地址", map[string]string{"ip": value})
			return
		}
		if !unique[ip] {
			unique[ip] = true
			ips = append(ips, ip)
		} else {
			duplicates++
		}
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		dbError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	added := 0
	for _, ip := range ips {
		result, execErr := tx.Exec(r.Context(), "INSERT INTO ip_blacklist(ip) VALUES($1::inet) ON CONFLICT(ip) DO NOTHING", ip)
		if execErr != nil {
			dbError(w, execErr)
			return
		}
		added += int(result.RowsAffected())
	}
	if err = tx.Commit(r.Context()); err != nil {
		dbError(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]int{"added": added, "skipped": len(ips) - added + duplicates})
}

func (a *app) deleteBlacklist(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IP string `json:"ip"`
	}
	if !decode(w, r, &input) {
		return
	}
	ip, ok := canonicalIP(input.IP)
	if !ok {
		apiError(w, http.StatusUnprocessableEntity, "黑名单只接受单个 IPv4 或 IPv6 地址", map[string]string{"ip": input.IP})
		return
	}
	_, err := a.db.Exec(r.Context(), "DELETE FROM ip_blacklist WHERE ip=$1::inet", ip)
	if err != nil {
		dbError(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]bool{"ok": true})
}

// blacklistIPs returns only addresses that fall inside the requested ranges.
func (a *app) blacklistIPs(ctx context.Context, cidrs []string) ([]string, error) {
	rows, err := a.db.Query(ctx, "SELECT host(ip) FROM ip_blacklist WHERE ip <<= ANY($1::cidr[])", cidrs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ips := []string{}
	for rows.Next() {
		var ip string
		if err = rows.Scan(&ip); err != nil {
			return nil, err
		}
		ips = append(ips, ip)
	}
	return ips, rows.Err()
}

func (a *app) scanIPsExcludingBlacklist(ctx context.Context, cidrs []string) ([]netip.Addr, error) {
	blocked, err := a.blacklistIPs(ctx, cidrs)
	if err != nil {
		return nil, err
	}
	total, targets, err := scanTargetsExcluding(cidrs, blocked)
	if err != nil {
		return nil, err
	}
	if total > 4096 {
		return nil, errors.New("所选网段去重后超过 4096 个 IP，请减少选择")
	}
	ips := make([]netip.Addr, 0)
	for ip := range targets {
		ips = append(ips, ip)
	}
	return ips, nil
}
