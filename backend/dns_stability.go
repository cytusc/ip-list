package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// dnsP95Mean removes successful samples above the interpolated 95th percentile.
func dnsP95Mean(samples []dnsSample) (float64, bool) {
	delays := make([]float64, 0, len(samples))
	for _, s := range samples {
		if s.Success && s.Delay >= 0 && !math.IsNaN(s.Delay) && !math.IsInf(s.Delay, 0) {
			delays = append(delays, s.Delay)
		}
	}
	if len(delays) == 0 {
		return 0, false
	}
	sort.Float64s(delays)
	cutoff := delays[len(delays)-1]
	if len(delays) >= 5 {
		position := 0.95 * float64(len(delays)-1)
		lower := int(position)
		cutoff = delays[lower] + (delays[lower+1]-delays[lower])*(position-float64(lower))
	}
	end := sort.Search(len(delays), func(i int) bool { return delays[i] > cutoff })
	if end == 0 {
		end = 1
	}
	total := 0.0
	for _, delay := range delays[:end] {
		total += delay
	}
	return total / float64(end), true
}

func dnsAsiaPacific(country string) bool {
	switch strings.ToUpper(country) {
	case "AF", "AM", "AS", "AU", "AZ", "BD", "BN", "BT", "CC", "CK", "CN", "CX", "FJ", "FM", "GE", "GU", "HK", "ID", "IN", "IO", "JP", "KG", "KH", "KI", "KP", "KR", "KZ", "LA", "LK", "MH", "MM", "MN", "MO", "MP", "MV", "MY", "NC", "NF", "NP", "NR", "NU", "NZ", "PF", "PG", "PH", "PK", "PW", "SB", "SG", "TH", "TJ", "TK", "TL", "TM", "TO", "TV", "TW", "UZ", "VN", "VU", "WF", "WS":
		return true
	default:
		return false
	}
}

// Compare IP averages against the fastest country's average in each pool.
func dnsSelectLatency(candidates []dnsCandidate) {
	type group struct{ pool, country string }
	totals, counts := map[group]float64{}, map[group]int{}
	for _, item := range candidates {
		if item.State != "healthy" || dnsCountry(item.Country) == "" || item.Delay < 0 || math.IsNaN(item.Delay) || math.IsInf(item.Delay, 0) {
			continue
		}
		key := group{item.Pool, strings.ToUpper(item.Country)}
		totals[key] += item.Delay
		counts[key]++
	}
	baseline := map[string]float64{}
	for key, total := range totals {
		average := total / float64(counts[key])
		current, ok := baseline[key.pool]
		if !ok || average < current {
			baseline[key.pool] = average
		}
	}
	for i := range candidates {
		item := &candidates[i]
		minimum, ok := baseline[item.Pool]
		if item.State != "healthy" || !ok {
			continue
		}
		margin := 20.0
		if dnsAsiaPacific(item.Country) {
			margin = 10
		}
		if item.Delay > minimum+margin {
			item.State = "pending"
			item.Reason = fmt.Sprintf("平均延迟 %.1f ms，超过最低地区均值 %.1f ms + %.0f ms", item.Delay, minimum, margin)
		}
	}
}
