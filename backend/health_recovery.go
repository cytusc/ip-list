package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type healthRecovery struct {
	Since        time.Time `json:"since"`
	CountedAt    time.Time `json:"counted_at"`
	Rounds       int       `json:"rounds"`
	UnknownSince time.Time `json:"unknown_since"`
	Quarantined  bool      `json:"quarantined"`
}

// Count spaced rounds, never repeated previews or simultaneous background jobs.
func (h *healthRecovery) observe(status string, at time.Time, control bool) bool {
	switch status {
	case "passed":
		if h.Since.IsZero() || at.Sub(h.CountedAt) > 15*time.Minute {
			h.Since, h.CountedAt, h.Rounds = at, at, 1
		} else if at.Sub(h.CountedAt) >= 5*time.Minute {
			h.CountedAt, h.Rounds = at, h.Rounds+1
		}
		if h.Rounds >= 3 && at.Sub(h.Since) >= 10*time.Minute {
			h.UnknownSince = time.Time{}
			h.Quarantined = false
			return true
		}
	case "failed", "unknown":
		h.Since, h.CountedAt, h.Rounds = time.Time{}, time.Time{}, 0
		if status == "failed" {
			h.Quarantined = true
		}
		if h.UnknownSince.IsZero() {
			h.UnknownSince = at
		}
		if status == "unknown" && control && at.Sub(h.UnknownSince) >= 30*time.Minute {
			h.Quarantined = true
		}
	}
	return false
}

// Caller holds the pool lock and commits latest evidence and recovery together.
func saveHealthRecovery(ctx context.Context, tx pgx.Tx, pool, hash string, at time.Time, candidates []dnsCandidate, statuses []string) error {
	type previous struct {
		hash  string
		at    time.Time
		state healthRecovery
	}
	old := map[string]previous{}
	rows, err := tx.Query(ctx, `SELECT host(ip),samples_hash,checked_at,state FROM ip_health_recovery WHERE pool=$1`, pool)
	if err != nil {
		return err
	}
	for rows.Next() {
		var ip string
		var p previous
		var raw []byte
		if err = rows.Scan(&ip, &p.hash, &p.at, &raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &p.state); err != nil {
			break
		}
		old[ip] = p
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	type saved struct {
		IP      string         `json:"ip"`
		State   healthRecovery `json:"state"`
		Ready   bool           `json:"ready"`
		Status  string         `json:"status"`
		Details dnsCandidate   `json:"details"`
	}
	records := []saved{}
	for i, status := range statuses {
		if status == "" {
			continue
		}
		item := &candidates[i]
		prior := old[item.IP]
		if prior.at.After(at) {
			return fmt.Errorf("检测结果已更新，等待重新验证")
		}
		h := prior.state
		if prior.hash != hash {
			h = healthRecovery{}
		}
		ready := h.observe(status, at, item.HealthControl)
		records = append(records, saved{item.IP, h, ready, status, *item})
		if h.Quarantined {
			if item.State != "failed" {
				item.State, item.Reason = "pending", "此前故障尚未完成恢复观察，暂停发布"
			}
		} else if status == "passed" && !ready && item.State == "healthy" {
			item.State, item.Reason = "unknown", "恢复观察：需连续通过 3 轮、每轮间隔至少 5 分钟、覆盖至少 10 分钟"
		}
	}
	raw, err := json.Marshal(records)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `WITH records AS MATERIALIZED (
 SELECT * FROM jsonb_to_recordset($1::jsonb) r(ip text,state jsonb,ready boolean,status text,details jsonb)
 ), history AS (
 INSERT INTO ip_health_checks(pool,ip,checked_at,samples_hash,status,details)
 SELECT $2,ip::inet,$4,$3,status,details FROM records ON CONFLICT DO NOTHING
 ) INSERT INTO ip_health_recovery(pool,ip,samples_hash,checked_at,ready,state)
 SELECT $2,ip::inet,$3,$4,ready,state FROM records ON CONFLICT(pool,ip) DO UPDATE SET
 samples_hash=excluded.samples_hash,checked_at=excluded.checked_at,ready=excluded.ready,state=excluded.state`, string(raw), pool, hash, at)
	return err
}

func (a *app) dnsCheckWebsiteHealth(ctx context.Context, c dnsConfig, items []dnsCandidate) error {
	at := time.Now().UTC()
	dnsCheckWebsiteHTTPS(ctx, c, items)
	states := make([]string, len(items))
	for i, item := range items {
		if item.Pool != "cf" {
			continue
		}
		switch item.HTTPSState {
		case "healthy":
			states[i] = "passed"
		case "failed":
			states[i] = "failed"
		case "unknown":
			states[i] = "unknown"
		}
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7021082)`); err != nil {
		return err
	}
	var current bool
	if err = tx.QueryRow(ctx, `SELECT COALESCE(config->>'website_url','')=$1 FROM dns_settings WHERE id`, c.WebsiteURL).Scan(&current); err != nil {
		return err
	}
	if !current {
		return fmt.Errorf("网站检测地址已改变，等待重新验证")
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(c.WebsiteURL)))
	if err = saveHealthRecovery(ctx, tx, "cf", hash, at, items, states); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Recovery must progress even when the DNS publishing interval exceeds 15 minutes.
func (a *app) recheckWebsiteHealth(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var raw []byte
	if err := a.db.QueryRow(ctx, `SELECT config FROM dns_settings WHERE id`).Scan(&raw); err != nil {
		return err
	}
	var c dnsConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	if !c.WebsiteEnabled || c.WebsiteURL == "" {
		return nil
	}
	rows, err := a.db.Query(ctx, `SELECT host(ip) FROM cf_ip_pool p WHERE available AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=p.ip) ORDER BY ip`)
	if err != nil {
		return err
	}
	items := []dnsCandidate{}
	for rows.Next() {
		item := dnsCandidate{Pool: "cf", State: "healthy"}
		if err = rows.Scan(&item.IP); err != nil {
			break
		}
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	return a.dnsCheckWebsiteHealth(ctx, c, items)
}
