package main

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"time"
)

// A fresh application check supplements (never replaces) probe stability.
// Both requests retain the business Host/SNI, verify TLS and pin the TCP IP.
// Bound this phase so slow endpoints cannot consume the entire DNS deadline.
func dnsCheckWebsiteHTTPS(ctx context.Context, c dnsConfig, candidates []dnsCandidate) {
	check := func(ctx context.Context, ip, target string) error {
		addr, err := netip.ParseAddr(ip)
		if err != nil || !publicMetadataPrefix(netip.PrefixFrom(addr, addr.BitLen())) {
			return fmt.Errorf("非公网地址")
		}
		_, err = probeCF(ctx, addr, target, nil)
		return err
	}
	dnsCheckWebsiteWith(ctx, c, candidates, check)
}

func dnsCheckWebsiteWith(ctx context.Context, c dnsConfig, candidates []dnsCandidate, check func(context.Context, string, string) error) {
	phase, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	type result struct {
		passed, attempted int
		reason            string
		details           []string
	}
	results := make([]result, len(candidates))
	jobs := make(chan int, len(candidates))
	for i := range candidates {
		if candidates[i].Pool == "cf" {
			jobs <- i
		}
	}
	close(jobs)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				for attempt := 0; attempt < 2 && phase.Err() == nil && c.WebsiteURL != ""; attempt++ {
					attemptCtx, stop := context.WithTimeout(phase, 4*time.Second)
					err := check(attemptCtx, candidates[i].IP, c.WebsiteURL)
					stop()
					if phase.Err() != nil {
						break
					}
					results[i].attempted++
					if err == nil {
						results[i].passed++
						results[i].details = append(results[i].details, "passed")
					} else {
						results[i].reason = err.Error()
						results[i].details = append(results[i].details, err.Error())
					}
				}
			}
		}()
	}
	wg.Wait()
	// No healthy control endpoint means the checker/route/business origin may
	// be down. Treat failures as unknown rather than deleting the whole pool.
	controlOK := false
	for _, r := range results {
		controlOK = controlOK || r.passed == 2
	}
	for i := range candidates {
		item := &candidates[i]
		if item.Pool != "cf" {
			continue
		}
		r := results[i]
		item.HealthControl = controlOK
		item.HTTPSAttempts = r.details
		item.HTTPSURL = c.WebsiteURL
		switch {
		case r.passed == 2:
			item.HTTPSState = "healthy"
			item.HTTPSReason = "实际业务 HTTPS 连续两次通过，证书及 HTTP 状态正常"
		case r.attempted == 2 && r.passed == 0 && controlOK:
			item.HTTPSState, item.State = "failed", "failed"
			item.HTTPSReason = "实际业务 HTTPS 连续两次失败：" + r.reason
			item.Reason = item.HTTPSReason
		default:
			item.HTTPSState = "unknown"
			item.HTTPSReason = "HTTPS 检测未连续通过，或没有健康对照 IP，保留已有解析并暂停新增"
			if item.State != "failed" && item.State != "pending" {
				item.State, item.Reason = "unknown", item.HTTPSReason
			}
		}
	}
}
