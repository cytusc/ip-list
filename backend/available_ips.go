package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type ipCheck struct {
	Protocol  string    `json:"protocol"`
	Delay     int       `json:"delay_ms"`
	NodeID    string    `json:"node_id"`
	NodeName  string    `json:"node_name"`
	CheckedAt time.Time `json:"checked_at"`
}

type availableIP struct {
	ASN         string         `json:"asn"`
	Provider    string         `json:"provider"`
	IP          string         `json:"ip"`
	Checks      []ipCheck      `json:"checks"`
	ProbeDelays []probeIPDelay `json:"probe_delays"`
}

type probeIPDelay struct {
	Samples   []latencyProbeResult `json:"samples"`
	Online    bool                 `json:"online"`
	Fresh     bool                 `json:"fresh"`
	ProbeID   string               `json:"probe_id"`
	Name      string               `json:"name"`
	DelayMS   *float64             `json:"delay_ms"`
	Success   *bool                `json:"success"`
	CheckedAt *time.Time           `json:"checked_at"`
}

// One bulk write per round; completed probes also survive a manual stop.
// In-flight canceled probes are absent from Results and never invalidate an IP.
func (a *app) saveIPResults(ctx context.Context, job scanJob) error {
	if len(job.Results) == 0 || job.ConfigHash == "" || job.FinishedAt == nil {
		return nil
	}
	data, err := json.Marshal(job.Results)
	if err != nil {
		return err
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
 INSERT INTO ip_results(node_id,ip,config_hash,protocol,available,delay_ms,checked_at)
 SELECT n.id,r.ip::inet,$2,$3,r.status='success',r.delay_ms,$5
 FROM jsonb_to_recordset($4::jsonb) AS r(ip text,status text,delay_ms integer)
 JOIN nodes n ON n.id=$1::uuid AND md5(n.share_uri)=$2
 ON CONFLICT(node_id,ip) DO UPDATE SET
 config_hash=excluded.config_hash,protocol=excluded.protocol,
 available=excluded.available,delay_ms=excluded.delay_ms,checked_at=excluded.checked_at
 WHERE ip_results.checked_at <= excluded.checked_at`,
		job.Input.NodeID, job.ConfigHash, job.Protocol, string(data), job.FinishedAt)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at)
 SELECT $1,r.ip::inet,r.status='success',r.delay_ms,$3
 FROM jsonb_to_recordset($2::jsonb) AS r(ip text,status text,delay_ms integer)
 ON CONFLICT(protocol,ip) DO UPDATE SET available=excluded.available,
 delay_ms=excluded.delay_ms,checked_at=excluded.checked_at
 WHERE node_ip_pool.checked_at<=excluded.checked_at`, job.Protocol, string(data), job.FinishedAt)
	if err != nil {
		return err
	}
	if err = a.disableEmptyNetworks(ctx, tx, job); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	// The same round is kept as a record so it can be reopened from the list.
	return a.saveScanRecord(ctx, &job, data)
}

func (a *app) availableIPs(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `
 SELECT host(s.ip),s.protocol,s.delay_ms, '', '协议共用 IP 池',s.checked_at,COALESCE(info.asn,''),COALESCE(info.provider,'')
 FROM node_ip_pool s `+networkInfoJoin+`
 WHERE s.available AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=s.ip)
 ORDER BY s.ip,s.protocol`)
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	items := []availableIP{}
	for rows.Next() {
		var ip, asn, provider string
		var check ipCheck
		if err = rows.Scan(&ip, &check.Protocol, &check.Delay, &check.NodeID, &check.NodeName, &check.CheckedAt, &asn, &provider); err != nil {
			dbError(w, err)
			return
		}
		if len(items) == 0 || items[len(items)-1].IP != ip {
			items = append(items, availableIP{IP: ip, ASN: asn, Provider: provider, Checks: []ipCheck{}})
		}
		last := &items[len(items)-1]
		last.Checks = append(last.Checks, check)
	}
	if err = rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	probeRows, err := a.db.Query(r.Context(), `WITH targets AS (
 SELECT DISTINCT ip FROM node_ip_pool WHERE available
 AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=node_ip_pool.ip)
), latest AS (
 SELECT DISTINCT ON (probe_id,ip) probe_id,ip,delay_ms,success,checked_at
 FROM latency_probe_results WHERE checked_at>now()-interval '30 days'
 ORDER BY probe_id,ip,checked_at DESC,id DESC
)
SELECT host(t.ip),p.id::text,p.name,l.delay_ms,l.success,l.checked_at,`+probeOnlineSQL+`,
COALESCE(l.checked_at>now()-make_interval(secs=>`+probeSampleWindowSQL+`*2),false),
COALESCE(recent.samples,'[]'::jsonb)
FROM latency_probes p CROSS JOIN targets t
LEFT JOIN latest l ON l.probe_id=p.id AND l.ip=t.ip
LEFT JOIN LATERAL (
 SELECT jsonb_agg(sample ORDER BY sample.checked_at,sample.id) AS samples FROM (
  SELECT id,host(ip) AS ip,delay_ms,success,checked_at FROM latency_probe_results
  WHERE probe_id=p.id AND ip=t.ip AND checked_at>now()-interval '30 days'
  ORDER BY checked_at DESC,id DESC LIMIT 16
 ) sample
) recent ON true
WHERE p.enabled ORDER BY t.ip,p.created_at`)
	if err != nil {
		dbError(w, err)
		return
	}
	defer probeRows.Close()
	itemsByIP := make(map[string]*availableIP, len(items))
	for i := range items {
		itemsByIP[items[i].IP] = &items[i]
		items[i].ProbeDelays = []probeIPDelay{}
	}
	for probeRows.Next() {
		var ip string
		var delay probeIPDelay
		var samples []byte
		if err = probeRows.Scan(&ip, &delay.ProbeID, &delay.Name, &delay.DelayMS, &delay.Success, &delay.CheckedAt, &delay.Online, &delay.Fresh, &samples); err != nil {
			dbError(w, err)
			return
		}
		if err = json.Unmarshal(samples, &delay.Samples); err != nil {
			dbError(w, err)
			return
		}
		if item := itemsByIP[ip]; item != nil {
			item.ProbeDelays = append(item.ProbeDelays, delay)
		}
	}
	if err = probeRows.Err(); err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, items)
}

// IP ownership metadata is shared across node templates. Prefer the most
// specific enabled range with metadata; leave unknown information empty.
const networkInfoJoin = ` LEFT JOIN LATERAL (
 SELECT w.asn,w.provider FROM networks w
 WHERE w.enabled AND s.ip <<= w.cidr AND (w.asn<>'' OR w.provider<>'')
 ORDER BY masklen(w.cidr) DESC,w.updated_at DESC,w.id LIMIT 1
) info ON true `
