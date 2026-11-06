package main

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"time"
)

func (a *app) dnsCheckNodes(ctx context.Context, candidates []dnsCandidate) error {
	needed := false
	for _, item := range candidates {
		needed = needed || (item.Pool != "cf" && item.State != "failed")
	}
	if !needed {
		return nil
	}
	started := time.Now().UTC()
	rows, err := a.db.Query(ctx, `SELECT share_uri,
 (SELECT md5(string_agg(config_hash,',' ORDER BY config_hash)) FROM node_grpc_samples)
 FROM node_grpc_samples ORDER BY config_hash`)
	if err != nil {
		return err
	}
	var templates []map[string]any
	var samplesHash string
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw, &samplesHash); err != nil {
			break
		}
		template, _, parseErr := parseShare(raw)
		if parseErr != nil {
			rows.Close()
			return fmt.Errorf("已启用的 gRPC 节点配置无效，请检查节点设置")
		}
		templates = append(templates, template)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	check := func(ctx context.Context, template map[string]any, ip netip.Addr) (*certificateCheck, error) {
		if env("PROBE_KERNEL", "mihomo") == "xray" {
			if !acquireXrayScanSlot(ctx) {
				return nil, ctx.Err()
			}
			defer func() { <-xrayScanSlots }()
		}
		certificate, err := checkCertificate(ctx, template, ip)
		if err == nil {
			_, err = probeIP(ctx, template, ip, "https://www.gstatic.com/generate_204")
		}
		return certificate, err
	}
	states := dnsCheckNodesWith(ctx, templates, candidates, check)
	return a.saveNodeHealth(ctx, samplesHash, started, candidates, states)
}

// Shared DNS entrances must work for every enabled gRPC sample, with its own SNI.
// Old scan evidence and ICMP success cannot establish current TLS/proxy health.
func dnsCheckNodesWith(ctx context.Context, templates []map[string]any, candidates []dnsCandidate, check func(context.Context, map[string]any, netip.Addr) (*certificateCheck, error)) []string {
	phase, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	type result struct {
		attempted, passed int
		invalid           bool
	}
	type job struct{ candidate, node int }
	results := make([][]result, len(candidates))
	jobs := make(chan job)
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := range jobs {
				r := &results[j.candidate][j.node]
				ip, err := netip.ParseAddr(candidates[j.candidate].IP)
				if err != nil || !publicMetadataPrefix(netip.PrefixFrom(ip, ip.BitLen())) {
					continue
				}
				for attempt := 0; attempt < 2 && phase.Err() == nil; attempt++ {
					attemptCtx, stop := context.WithTimeout(phase, 8*time.Second)
					certificate, err := check(attemptCtx, templates[j.node], ip)
					stop()
					if certificate != nil && certificate.Status == "invalid" {
						r.invalid = true
						break
					}
					if phase.Err() != nil {
						break
					}
					r.attempted++
					if err == nil && certificate != nil && (certificate.Status == "passed" || certificate.Status == "not_required") {
						r.passed++
					}
				}
			}
		}()
	}
	states := make([]string, len(candidates))
	for i := range candidates {
		if candidates[i].Pool == "cf" || candidates[i].State == "failed" {
			continue
		}
		results[i] = make([]result, len(templates))
		for node := range templates {
			select {
			case jobs <- job{i, node}:
			case <-phase.Done():
			}
		}
	}
	close(jobs)
	workers.Wait()
	controls := make([]bool, len(templates))
	for _, row := range results {
		for node, r := range row {
			controls[node] = controls[node] || r.passed == 2
		}
	}
	for i := range candidates {
		item := &candidates[i]
		if item.Pool == "cf" || item.State == "failed" {
			continue
		}
		passed := len(templates) > 0
		attempted := false
		failed, invalid := false, false
		for node, r := range results[i] {
			attempted = attempted || r.attempted > 0 || r.invalid
			passed = passed && r.passed == 2
			invalid = invalid || r.invalid
			failed = failed || (r.attempted == 2 && r.passed == 0 && controls[node])
		}
		switch {
		case invalid:
			states[i] = "failed"
			item.State, item.Reason = "failed", "DNS 发布前复查：节点 SNI 证书验证失败"
		case failed:
			states[i] = "failed"
			item.State, item.Reason = "failed", "DNS 发布前复查：同一 gRPC 节点有健康对照 IP，本 IP 连续两次连接失败"
		case !passed:
			// Leave unstarted work at the front of the next background recheck.
			if attempted {
				states[i] = "unknown"
			}
			item.State, item.Reason = "unknown", "DNS 发布前复查：未通过全部已启用 gRPC 节点的两次检测，保留已有解析并暂停新增"
		default:
			states[i] = "passed"
		}
	}
	return states
}
