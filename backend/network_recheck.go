package main

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

type networkRecheckItem struct {
	Protocol string `json:"protocol"`
	JobID    string `json:"job_id,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (a *app) recheckNetwork(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if validate.Var(id, "uuid") != nil {
		apiError(w, 422, "网段 ID 无效", nil)
		return
	}
	var cidr string
	var enabled bool
	err := a.db.QueryRow(r.Context(), "SELECT cidr::text,enabled FROM networks WHERE id=$1", id).Scan(&cidr, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, 404, "网段不存在", nil)
		return
	}
	if err != nil {
		dbError(w, err)
		return
	}
	if !enabled {
		apiError(w, 422, "请先启用该网段后再重新检测", nil)
		return
	}
	ips, err := expandPrefixes([]string{cidr})
	if err != nil {
		apiError(w, 422, err.Error(), nil)
		return
	}
	rows, err := a.db.Query(r.Context(), "SELECT id::text,name,share_uri FROM nodes WHERE enabled AND share_uri<>'' ORDER BY created_at,id")
	if err != nil {
		dbError(w, err)
		return
	}
	type sample struct {
		id, name, raw, protocol string
		template                map[string]any
	}
	samples := []sample{}
	seen := map[string]bool{}
	for rows.Next() {
		var node sample
		if err = rows.Scan(&node.id, &node.name, &node.raw); err != nil {
			rows.Close()
			dbError(w, err)
			return
		}
		node.protocol = nodePoolProtocol(node.raw)
		if seen[node.protocol] {
			continue
		}
		template, _, parseErr := parseShare(node.raw)
		if parseErr != nil {
			continue
		}
		node.template = template
		seen[node.protocol] = true
		samples = append(samples, node)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		dbError(w, err)
		return
	}
	if len(samples) == 0 {
		apiError(w, 422, "没有可用的协议样本，请先添加并启用有效的 VLESS 节点", nil)
		return
	}
	items := []networkRecheckItem{}
	for _, node := range samples {
		input := scanInput{NodeID: node.id, NetworkIDs: []string{id}, URL: "https://www.gstatic.com/generate_204", Timeout: 5000, Concurrency: 5}
		job, _, message := a.launchNodeScan(r.Context(), r.Context().Value(userContextKey{}).(user).ID, input, node.raw, node.name, node.template, []string{cidr}, ips, true)
		items = append(items, networkRecheckItem{Protocol: node.protocol, JobID: job.ID, Error: message})
	}
	respond(w, 202, items)
}
