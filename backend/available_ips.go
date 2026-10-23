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
	IP     string    `json:"ip"`
	Checks []ipCheck `json:"checks"`
}

// One bulk write per round; completed probes also survive a manual stop.
// In-flight canceled probes are absent from Results and never invalidate an IP.
func (a *app) saveIPResults(ctx context.Context, job scanJob) error {
	if len(job.Results) == 0 || job.ConfigHash == "" {
		return nil
	}
	data, err := json.Marshal(job.Results)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(ctx, `
 INSERT INTO ip_results(node_id,ip,config_hash,protocol,available,delay_ms,checked_at)
 SELECT n.id,r.ip::inet,$2,$3,r.status='success',r.delay_ms,$5
 FROM jsonb_to_recordset($4::jsonb) AS r(ip text,status text,delay_ms integer)
 JOIN nodes n ON n.id=$1::uuid AND md5(n.share_uri)=$2
 ON CONFLICT(node_id,ip) DO UPDATE SET
 config_hash=excluded.config_hash,protocol=excluded.protocol,
 available=excluded.available,delay_ms=excluded.delay_ms,checked_at=excluded.checked_at
 WHERE ip_results.checked_at <= excluded.checked_at`,
		job.Input.NodeID, job.ConfigHash, job.Protocol, string(data), job.FinishedAt)
	return err
}

func (a *app) availableIPs(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `
 SELECT host(s.ip),s.protocol,s.delay_ms,n.id::text,n.name,s.checked_at
 FROM ip_results s JOIN nodes n ON n.id=s.node_id
 WHERE s.available AND n.enabled AND s.config_hash=md5(n.share_uri)
 ORDER BY s.ip,s.protocol,s.delay_ms,n.name`)
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	items := []availableIP{}
	for rows.Next() {
		var ip string
		var check ipCheck
		if err = rows.Scan(&ip, &check.Protocol, &check.Delay, &check.NodeID, &check.NodeName, &check.CheckedAt); err != nil {
			dbError(w, err)
			return
		}
		if len(items) == 0 || items[len(items)-1].IP != ip {
			items = append(items, availableIP{IP: ip, Checks: []ipCheck{}})
		}
		last := &items[len(items)-1]
		last.Checks = append(last.Checks, check)
	}
	if err = rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, items)
}
