package main

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"golang.org/x/text/language"
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
	region, err := language.ParseRegion(country)
	return err == nil && (language.MustParseRegion("142").Contains(region) || language.MustParseRegion("009").Contains(region))
}

// Each country uses its fastest IP's trimmed mean, independently per pool.
func dnsSelectLatency(candidates []dnsCandidate) {
	type group struct{ pool, country string }
	baseline := map[group]float64{}
	for _, item := range candidates {
		if item.State != "healthy" || dnsCountry(item.Country) == "" || item.Delay < 0 || math.IsNaN(item.Delay) || math.IsInf(item.Delay, 0) {
			continue
		}
		key := group{item.Pool, strings.ToUpper(item.Country)}
		current, ok := baseline[key]
		if !ok || item.Delay < current {
			baseline[key] = item.Delay
		}
	}
	for i := range candidates {
		item := &candidates[i]
		minimum, ok := baseline[group{item.Pool, strings.ToUpper(item.Country)}]
		if item.State != "healthy" || !ok {
			continue
		}
		margin := 20.0
		if dnsAsiaPacific(item.Country) {
			margin = 10
		}
		item.Reason = fmt.Sprintf("P95 去峰平均延迟 %.1f ms，%s 地区最低 %.1f ms，上限 %.1f ms", item.Delay, strings.ToUpper(item.Country), minimum, minimum+margin)
		if item.Delay > minimum+margin {
			item.State = "pending"
			item.Reason = "超过地区延迟门槛：" + item.Reason
		}
	}
}
