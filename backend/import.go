package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func (a *app) importNodes(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Links string `json:"links"`
	}
	if !decode(w, r, &input) {
		return
	}
	lines := strings.Split(strings.TrimSpace(input.Links), "\n")
	if len(lines) > 20 || strings.TrimSpace(input.Links) == "" {
		apiError(w, 422, "每次请输入 1–20 条链接，每行一条", nil)
		return
	}
	configs := []ConfigInput{}
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		_, u, err := parseShare(line)
		if err != nil {
			apiError(w, 422, fmt.Sprintf("第 %d 行：%s", i+1, err), nil)
			return
		}
		name := u.Fragment
		if name == "" {
			name = u.Hostname() + "-" + u.Query().Get("type")
		}
		port, _ := strconv.Atoi(u.Port())
		in := ConfigInput{Name: name, Address: u.Hostname(), Port: port, Enabled: true, ShareURI: line}
		if fields := in.validate("nodes"); len(fields) > 0 {
			apiError(w, 422, fmt.Sprintf("第 %d 行：名称或链接无效（名称最多 80 字符）", i+1), fields)
			return
		}
		configs = append(configs, in)
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		dbError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	for _, in := range configs {
		_, err = tx.Exec(r.Context(), "INSERT INTO nodes(name,address,port,enabled,share_uri) VALUES($1,$2,$3,true,$4)", in.Name, in.Address, in.Port, in.ShareURI)
		if err != nil {
			dbError(w, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		dbError(w, err)
		return
	}
	respond(w, 201, map[string]int{"count": len(configs)})
}
