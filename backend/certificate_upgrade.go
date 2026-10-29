package main

import (
	"context"
	"log"
	"net/netip"
	"sync"
	"time"
)

// Recheck historical successes once after upgrading. Until this finishes the
// current pool view hides them, so unchecked records cannot bypass admission.
// Normal scan timestamp/configuration guards also protect these writes.
func (a *app) revalidateLegacyCertificates(ctx context.Context) {
	rows, err := a.db.Query(ctx, `SELECT p.protocol,host(p.ip),p.config_hash,n.id::text,n.share_uri
 FROM node_ip_pool p
 JOIN LATERAL (SELECT id,share_uri FROM nodes WHERE enabled AND share_uri<>'' AND md5(share_uri)=p.config_hash ORDER BY id LIMIT 1) n ON true
 WHERE p.protocol='grpc' AND p.available AND p.certificate='{}'::jsonb
 AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=p.ip)
 ORDER BY p.ip,p.protocol`)
	if err != nil {
		log.Printf("读取历史证书复检任务失败: %v", err)
		return
	}
	type candidate struct{ protocol, ip, hash, nodeID, raw string }
	targets := []candidate{}
	for rows.Next() {
		var target candidate
		if err = rows.Scan(&target.protocol, &target.ip, &target.hash, &target.nodeID, &target.raw); err != nil {
			break
		}
		targets = append(targets, target)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		log.Printf("读取历史证书复检任务失败: %v", err)
		return
	}
	if len(targets) == 0 {
		return
	}
	log.Printf("开始复检 %d 条历史可用 IP 的入口证书和代理连通性", len(targets))
	work := make(chan candidate)
	var workers sync.WaitGroup
	for i := 0; i < min(3, len(targets)); i++ {
		workers.Go(func() {
			for target := range work {
				if ctx.Err() != nil {
					return
				}
				template, _, err := parseShare(target.raw)
				if err != nil {
					continue
				}
				started := time.Now().UTC()
				checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				ip, err := netip.ParseAddr(target.ip)
				if err != nil {
					cancel()
					continue
				}
				certificate, err := checkCertificate(checkCtx, template, ip)
				delay := 0
				if err == nil {
					delay, err = probeIP(checkCtx, template, ip, "https://www.gstatic.com/generate_204")
				}
				cancel()
				if ctx.Err() != nil {
					return
				}
				result := scanResult{IP: target.ip, Status: "success", Delay: delay, Certificate: certificate}
				if err != nil {
					result.Status = "failed"
					result.Error = probeError(err)
					if certificate != nil && certificate.Status == "invalid" {
						result.Error = certificate.Error
					}
				}
				job := scanJob{Protocol: target.protocol, ConfigHash: target.hash, Input: scanInput{NodeID: target.nodeID}, FinishedAt: &started, Results: []scanResult{result}}
				if err = a.saveIPResults(ctx, job); err != nil {
					log.Printf("保存历史证书复检结果失败: %v", err)
				}
			}
		})
	}
send:
	for _, target := range targets {
		select {
		case work <- target:
		case <-ctx.Done():
			break send
		}
	}
	close(work)
	workers.Wait()
	log.Printf("历史可用 IP 证书复检结束")
}
