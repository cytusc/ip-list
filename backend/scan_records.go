package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Records keep the shape of one round, not of the whole task: a patrol writes a
// new row for every round so the list shows history instead of one moving job.
const scanRecordLimit = 100
const scanRecordResultRounds = 30

// A round that stops writing heartbeats was killed together with the process.
const scanRecordStatus = `CASE WHEN r.status='running' AND r.updated_at < now()-interval '60 seconds'
 THEN 'interrupted' ELSE r.status END`

type scanRecord struct {
	ID         int64        `json:"id"`
	JobID      string       `json:"job_id"`
	Round      int          `json:"round"`
	Protocol   string       `json:"protocol"`
	NodeName   string       `json:"node_name"`
	Scope      string       `json:"scope"`
	URL        string       `json:"url"`
	CIDRs      []string     `json:"cidrs"`
	Input      scanInput    `json:"input"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
	Total      int          `json:"total"`
	Completed  int          `json:"completed"`
	Success    int          `json:"success"`
	Status     string       `json:"status"`
	Error      string       `json:"error,omitempty"`
	Results    []scanResult `json:"results"`
	// Live marks a round whose counters and results came from the running task.
	Live      bool       `json:"live,omitempty"`
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
}

// The list only needs the summary plus a pointer to the round that is running.
type scanRecordList struct {
	Items  []scanRecord `json:"items"`
	Active *activeScan  `json:"active"`
}

type activeScan struct {
	JobID           string     `json:"job_id"`
	Protocol        string     `json:"protocol"`
	NodeName        string     `json:"node_name"`
	Round           int        `json:"round"`
	Status          string     `json:"status"`
	Total           int        `json:"total"`
	Completed       int        `json:"completed"`
	Success         int        `json:"success"`
	IntervalMinutes int        `json:"interval_minutes"`
	NextRunAt       *time.Time `json:"next_run_at,omitempty"`
}

// saveScanRecord writes the round row. Passing results==nil keeps the stored
// per-IP results untouched, so progress heartbeats never rewrite a large blob.
func (a *app) saveScanRecord(ctx context.Context, job *scanJob, results []byte) error {
	if a.db == nil || job.ID == "" || job.TargetID != "" || job.Protocol == "" || job.Status == "waiting" {
		return nil
	}
	round := max(job.Round, 1)
	cidrs := job.CIDRs
	if cidrs == nil {
		cidrs = []string{}
	}
	input, err := json.Marshal(job.Input)
	if err != nil {
		return err
	}
	args := []any{job.ID, round, job.Protocol, job.NodeName, job.Scope, job.URL, cidrs, string(input),
		job.StartedAt, job.FinishedAt, job.Total, job.Completed, job.Success, job.Status, job.Error}
	if results == nil {
		_, err = a.db.Exec(ctx, `INSERT INTO node_scan_rounds(job_id,round,protocol,node_name,scope,url,cidrs,input,started_at,finished_at,total,completed,success,status,error)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
 ON CONFLICT(job_id,round) DO UPDATE SET node_name=excluded.node_name,scope=excluded.scope,
  url=excluded.url,cidrs=excluded.cidrs,input=excluded.input,finished_at=excluded.finished_at,
  total=excluded.total,completed=excluded.completed,success=excluded.success,
  status=excluded.status,error=excluded.error,updated_at=now()`, args...)
		return err
	}
	_, err = a.db.Exec(ctx, `INSERT INTO node_scan_rounds(job_id,round,protocol,node_name,scope,url,cidrs,input,started_at,finished_at,total,completed,success,status,error,results)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
 ON CONFLICT(job_id,round) DO UPDATE SET node_name=excluded.node_name,scope=excluded.scope,
  url=excluded.url,cidrs=excluded.cidrs,input=excluded.input,finished_at=excluded.finished_at,
  total=excluded.total,completed=excluded.completed,success=excluded.success,
  status=excluded.status,error=excluded.error,results=excluded.results,updated_at=now()`,
		append(args, string(results))...)
	if err != nil {
		return err
	}
	a.pruneScanRecords(ctx, job.Protocol)
	return nil
}

// Drop finished rounds after a week and keep the per-IP payload of the newest
// rounds only; older records stay readable as summaries.
func (a *app) pruneScanRecords(ctx context.Context, protocol string) {
	_, _ = a.db.Exec(ctx, "DELETE FROM node_scan_rounds WHERE started_at < now()-interval '7 days'")
	_, _ = a.db.Exec(ctx, `UPDATE node_scan_rounds SET results='[]'::jsonb
 WHERE protocol=$1 AND jsonb_array_length(results)>0 AND id NOT IN (
  SELECT id FROM node_scan_rounds WHERE protocol=$1 ORDER BY started_at DESC,id DESC LIMIT $2)`,
		protocol, scanRecordResultRounds)
}

// A failed start must not leave a record that looks like a running scan.
func (a *app) discardScanRecord(ctx context.Context, jobID string) {
	if a.db == nil {
		return
	}
	_, _ = a.db.Exec(ctx, "DELETE FROM node_scan_rounds WHERE job_id=$1", jobID)
}

func (a *app) listScanRecords(w http.ResponseWriter, r *http.Request) {
	limit := scanRecordLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 && value <= 200 {
			limit = value
		}
	}
	protocol := r.URL.Query().Get("protocol")
	p, ok := a.listPage(w, r, min(limit, 50), "SELECT count(*) FROM node_scan_rounds WHERE ($1='' OR protocol=$1)", protocol)
	if !ok {
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT r.id,r.job_id,r.round,r.protocol,r.node_name,r.scope,r.url,r.cidrs,r.input,
 r.started_at,r.finished_at,r.total,r.completed,r.success,`+scanRecordStatus+`,r.error
 FROM node_scan_rounds r WHERE ($1='' OR r.protocol=$1)
 ORDER BY r.started_at DESC,r.id DESC LIMIT $2 OFFSET $3`, protocol, p.Size, p.Offset())
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	items := []scanRecord{}
	for rows.Next() {
		var item scanRecord
		if err = rows.Scan(&item.ID, &item.JobID, &item.Round, &item.Protocol, &item.NodeName, &item.Scope,
			&item.URL, &item.CIDRs, &item.Input, &item.StartedAt, &item.FinishedAt, &item.Total,
			&item.Completed, &item.Success, &item.Status, &item.Error); err != nil {
			dbError(w, err)
			return
		}
		item.Results = []scanResult{}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, scanRecordList{Items: items, Active: a.activeScan(r.Context())})
}

func (a *app) getScanRecord(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		apiError(w, 404, "检测记录不存在", nil)
		return
	}
	var item scanRecord
	paged := r.URL.Query().Get("paged") == "1"
	resultColumn := "r.results"
	if paged {
		resultColumn = "'[]'::jsonb"
	}
	err = a.db.QueryRow(r.Context(), `SELECT r.id,r.job_id,r.round,r.protocol,r.node_name,r.scope,r.url,r.cidrs,r.input,
 r.started_at,r.finished_at,r.total,r.completed,r.success,`+scanRecordStatus+`,r.error,`+resultColumn+`
 FROM node_scan_rounds r WHERE r.id=$1`, id).Scan(&item.ID, &item.JobID, &item.Round, &item.Protocol,
		&item.NodeName, &item.Scope, &item.URL, &item.CIDRs, &item.Input, &item.StartedAt, &item.FinishedAt,
		&item.Total, &item.Completed, &item.Success, &item.Status, &item.Error, &item.Results)
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, 404, "检测记录不存在或已清理", nil)
		return
	}
	if err != nil {
		dbError(w, err)
		return
	}
	a.attachLiveResults(r.Context(), &item)
	if paged {
		filter := r.URL.Query().Get("filter")
		if filter != "" && filter != "all" && filter != "success" && filter != "failed" {
			apiError(w, 422, "结果筛选无效", nil)
			return
		}
		if item.Live {
			results := make([]scanResult, 0, len(item.Results))
			for _, row := range item.Results {
				if filter == "" || filter == "all" || row.Status == filter {
					results = append(results, row)
				}
			}
			sort.Slice(results, func(i, j int) bool {
				a, b := results[i], results[j]
				if a.Status != b.Status {
					return a.Status == "success"
				}
				if a.Delay != b.Delay {
					return a.Delay < b.Delay
				}
				return a.IP < b.IP
			})
			page := 1
			if raw := r.URL.Query().Get("page"); raw != "" {
				var err error
				page, err = strconv.Atoi(raw)
				if err != nil || page < 1 {
					apiError(w, 422, "页码必须是正整数", nil)
					return
				}
			}
			page = min(page, max(1, (len(results)+49)/50))
			start := (page - 1) * 50
			item.Results = results[start:min(start+50, len(results))]
			w.Header().Set("X-Total-Count", strconv.Itoa(len(results)))
			w.Header().Set("X-Page", strconv.Itoa(page))
			w.Header().Set("X-Page-Size", "50")
		} else {
			const source = ` FROM node_scan_rounds r CROSS JOIN LATERAL jsonb_array_elements(r.results) e WHERE r.id=$1 AND ($2='' OR $2='all' OR e->>'status'=$2)`
			p, ok := a.listPage(w, r, 50, "SELECT count(*)"+source, id, filter)
			if !ok {
				return
			}
			var payload []byte
			err = a.db.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(e),'[]'::jsonb) FROM (SELECT e`+source+` ORDER BY (e->>'status'='success') DESC,(e->>'delay_ms')::integer,e->>'ip' LIMIT $3 OFFSET $4) selected`, id, filter, p.Size, p.Offset()).Scan(&payload)
			if err != nil {
				dbError(w, err)
				return
			}
			if err = json.Unmarshal(payload, &item.Results); err != nil {
				dbError(w, err)
				return
			}
		}
	}
	respond(w, 200, item)
}

// While a round runs, its per-IP results only exist in the task snapshot; the
// record itself is completed once the round ends.
func (a *app) attachLiveResults(ctx context.Context, item *scanRecord) {
	if item.Status != "running" || a.redis == nil {
		return
	}
	uid := ctx.Value(userContextKey{}).(user).ID
	data, err := a.redis.Get(ctx, scanKey(item.JobID)).Bytes()
	if err != nil {
		return
	}
	var job scanJob
	if json.Unmarshal(data, &job) != nil || job.UserID != uid || job.Round != item.Round {
		return
	}
	item.Completed, item.Success, item.Total = job.Completed, job.Success, job.Total
	item.Results, item.Status, item.FinishedAt = job.Results, job.Status, job.FinishedAt
	item.NextRunAt, item.Live = job.NextRunAt, true
}

// The task in flight is the only state that cannot be derived from records:
// between patrol rounds no record is running, yet the schedule still waits.
func (a *app) activeScan(ctx context.Context) *activeScan {
	uid := ctx.Value(userContextKey{}).(user).ID
	if a.redis == nil {
		return nil
	}
	id, err := a.redis.Get(ctx, latestKey(uid)).Result()
	if err != nil || id == "" {
		return nil
	}
	data, err := a.redis.Get(ctx, scanKey(id)).Bytes()
	if err != nil {
		return nil
	}
	var job scanJob
	if json.Unmarshal(data, &job) != nil || job.UserID != uid || job.TargetID != "" {
		return nil
	}
	if job.Status != "running" && job.Status != "waiting" {
		return nil
	}
	if time.Since(job.UpdatedAt) > 50*time.Second {
		return nil
	}
	return &activeScan{
		JobID: job.ID, Protocol: job.Protocol, NodeName: job.NodeName, Round: job.Round,
		Status: job.Status, Total: job.Total, Completed: job.Completed, Success: job.Success,
		IntervalMinutes: job.Input.IntervalMinutes, NextRunAt: job.NextRunAt,
	}
}
