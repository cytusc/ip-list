package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Evaluate each path independently. A bad path must not veto a healthy path,
// and missing observations must never count as evidence of recovery.
func dnsProbeStability(samples []dnsSample, now time.Time, freshness time.Duration) (string, bool) {
	recent := []dnsSample{}
	recovery := []dnsSample{}
	for _, s := range samples {
		if s.At.After(now) || s.At.Before(now.Add(-10*time.Minute)) {
			continue
		}
		s.Success = s.Success && s.Delay >= 0 && !math.IsNaN(s.Delay) && !math.IsInf(s.Delay, 0)
		recovery = append(recovery, s)
		if !s.At.Before(now.Add(-5 * time.Minute)) {
			recent = append(recent, s)
		}
	}
	sort.Slice(recovery, func(i, j int) bool { return recovery[i].At.Before(recovery[j].At) })
	sort.Slice(recent, func(i, j int) bool { return recent[i].At.Before(recent[j].At) })
	if len(recent) >= 6 && recent[len(recent)-1].At.Sub(recent[0].At) >= time.Minute && now.Sub(recent[len(recent)-1].At) <= freshness {
		loss, median, p95 := dnsStabilityStats(recent)
		if loss > 20 {
			return fmt.Sprintf("近期丢包过高：5 分钟丢包 %.1f%%，超过 20%%", loss), false
		}
		spikes := 0
		for _, s := range recent {
			if s.Success && s.Delay-median > math.Max(100, median) {
				spikes++
			}
		}
		if spikes >= 2 && p95-median > math.Max(100, median) {
			return fmt.Sprintf("近期延迟波动过大：中位 %.1f ms，P95 %.1f ms", median, p95), false
		}
	}
	if len(recovery) < 10 || recovery[0].At.After(now.Add(-10*time.Minute).Add(freshness)) || now.Sub(recovery[len(recovery)-1].At) > freshness || recovery[len(recovery)-1].At.Sub(recovery[0].At) < 9*time.Minute {
		return "", false
	}
	for i := 1; i < len(recovery); i++ {
		if recovery[i].At.Sub(recovery[i-1].At) > freshness {
			return "", false
		}
	}
	loss, median, p95 := dnsStabilityStats(recovery)
	return "", loss <= 10 && p95-median <= math.Max(60, median*0.5)
}

func dnsStabilityStats(samples []dnsSample) (loss, median, p95 float64) {
	delays := []float64{}
	for _, s := range samples {
		if s.Success {
			delays = append(delays, s.Delay)
		}
	}
	loss = 100 * float64(len(samples)-len(delays)) / float64(len(samples))
	if len(delays) > 0 {
		sort.Float64s(delays)
		median = delays[(len(delays)-1)/2]
		p95 = delays[int(math.Ceil(float64(len(delays))*0.95))-1]
	}
	return
}

// Published addresses use the removal threshold; additions use the stricter
// recovery threshold. This hysteresis is derived from raw samples and survives
// process restarts without adding a database migration.
func dnsStabilizeCandidates(c dnsConfig, candidates []dnsCandidate, remote []dnsRemoteRecord) {
	published := map[string]bool{}
	for _, r := range remote {
		prefix, ok := strings.CutSuffix(r.Name, "."+c.Domain)
		if !ok || r.Line != "default_view" || (r.Type != "A" && r.Type != "AAAA") {
			continue
		}
		pool := ""
		if prefix == "url" && c.WebsiteEnabled {
			pool = "cf"
		} else if dnsCountry(prefix) == "" {
			continue
		}
		for _, ip := range r.Values {
			published[pool+"/"+ip] = true
		}
	}
	for i := range candidates {
		item := &candidates[i]
		if item.State == "healthy" && !published[item.Pool+"/"+item.IP] && !item.StabilityReady {
			item.State = "pending"
			item.Reason = "等待连续 10 分钟稳定采样：丢包 ≤10%，P95 与中位延迟差 ≤max(60 ms, 中位延迟的 50%)"
		}
	}
}
