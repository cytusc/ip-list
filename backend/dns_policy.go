package main

import (
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/text/language"
)

type dnsConfig struct {
	WebsiteEnabled bool   `json:"website_enabled"`
	Domain         string `json:"domain"`
	Enabled        bool   `json:"enabled"`
	Interval       int    `json:"interval_seconds"`
	TTL            int32  `json:"ttl"`
	MaxIPs         int    `json:"max_ips"`
	Window         int    `json:"window_minutes"`
	MinSuccess     int    `json:"min_success_percent"`
	ProbeID        string `json:"probe_id"`
	Protocol       string `json:"protocol"`
}

func (c *dnsConfig) validate() error {
	c.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(c.Domain), "."))
	if len(c.Domain) > 253 || !regexp.MustCompile(`^([a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`).MatchString(c.Domain) {
		return fmt.Errorf("请输入有效的托管域名，例如 reiiv.com")
	}
	if c.Interval < 60 || c.Interval > 86400 || c.TTL < 60 || c.TTL > 86400 || c.MaxIPs < 1 || c.MaxIPs > 50 || c.Window < 2 || c.Window > 60 || c.MinSuccess < 0 || c.MinSuccess > 100 {
		return fmt.Errorf("更新间隔和 TTL 需为 60–86400 秒，每组 1–50 个 IP，观察窗口 2–60 分钟，成功率 1–100%%")
	}
	if c.ProbeID != "" && validate.Var(c.ProbeID, "uuid") != nil {
		return fmt.Errorf("请选择有效探针")
	}
	if c.Protocol != "" && !regexp.MustCompile(`^(vless|trojan)/(ws|grpc|tcp|xhttp|httpupgrade)$`).MatchString(c.Protocol) {
		return fmt.Errorf("请选择有效协议")
	}
	return nil
}

// Use ISO alpha-2 country/territory codes, including HK/MO/TW. Reject unknown
// or numeric regions rather than accidentally creating arbitrary DNS names.
func dnsCountry(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	region, err := language.ParseRegion(code)
	if err != nil || len(code) != 2 || !region.IsCountry() || region.String() != code {
		return ""
	}
	return strings.ToLower(code)
}

type dnsSample struct {
	At      time.Time
	Success bool
	Delay   float64
}

// Heartbeats do not prove that the probe's outbound network works. A failed
// target is actionable only if the same probe can still reach another target.
func dnsCorroborateFailure(state string, otherTargetOK bool) string {
	if state == "failed" && !otherTargetOK {
		return "unknown"
	}
	return state
}

// A probe must be online and have at least two fresh observations spanning
// a minute. Missing or sparse data is unknown, never evidence of health.
func dnsProbeHealth(samples []dnsSample, now time.Time, freshness time.Duration, c dnsConfig) (string, float64) {
	cutoff := now.Add(-time.Duration(c.Window) * time.Minute)
	valid := make([]dnsSample, 0, len(samples))
	for _, s := range samples {
		if !s.At.Before(cutoff) && !s.At.After(now) {
			valid = append(valid, s)
		}
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].At.Before(valid[j].At) })
	if len(valid) < 2 || now.Sub(valid[len(valid)-1].At) > freshness || valid[len(valid)-1].At.Sub(valid[0].At) < time.Minute {
		return "unknown", 0
	}
	count := 0
	total := 0.0
	var lastSuccess time.Time
	for _, s := range valid {
		if s.Success && s.Delay >= 0 && !math.IsNaN(s.Delay) && !math.IsInf(s.Delay, 0) {
			count++
			total += s.Delay
			lastSuccess = s.At
		}
	}
	latest := valid[len(valid)-1]
	if !latest.Success {
		failureStart := latest.At
		for i := len(valid) - 2; i >= 0; i-- {
			if valid[i].Success || valid[i+1].At.Sub(valid[i].At) > freshness {
				break
			}
			failureStart = valid[i].At
		}
		if latest.At.Sub(failureStart) >= time.Minute {
			return "failed", 0
		}
		// In continuous-failure mode, a recent success tolerates brief loss.
		// Missing reports are never evidence that an IP is still reachable.
		if c.MinSuccess > 0 || lastSuccess.IsZero() || now.Sub(lastSuccess) >= time.Minute || now.Sub(lastSuccess) > freshness {
			return "unknown", 0
		}
	}
	if lastSuccess.IsZero() || count*100 < len(valid)*c.MinSuccess {
		return "failed", 0
	}
	return "healthy", total / float64(count)
}

type dnsCandidate struct {
	Pool     string  `json:"pool,omitempty"`
	IP       string  `json:"ip"`
	Provider string  `json:"provider"`
	Country  string  `json:"country"`
	State    string  `json:"state"`
	Reason   string  `json:"reason"`
	Delay    float64 `json:"delay_ms"`
}

type dnsPlanRecord struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Country string   `json:"country"`
	Current []string `json:"current"`
	Desired []string `json:"desired"`
	Action  string   `json:"action"`
	Applied bool     `json:"applied"`
	Reason  string   `json:"reason"`
	ID      string   `json:"-"`
}

func dnsRecordKey(name, kind string) string {
	return strings.ToLower(strings.TrimSuffix(name, ".")) + "/" + kind
}

func dnsDesired(c dnsConfig, candidates []dnsCandidate) map[string]dnsPlanRecord {
	groups := map[string][]dnsCandidate{}
	for _, item := range candidates {
		ip, err := netip.ParseAddr(item.IP)
		country := dnsCountry(item.Country)
		if err != nil || !publicMetadataPrefix(netip.PrefixFrom(ip, ip.BitLen())) || country == "" || item.State != "healthy" {
			continue
		}
		kind := "A"
		if ip.Is6() {
			kind = "AAAA"
		}
		name := country + "." + c.Domain
		if item.Pool == "cf" {
			if !c.WebsiteEnabled || (country != "hk" && country != "jp" && country != "sg") || item.Delay < 0 || math.IsNaN(item.Delay) || math.IsInf(item.Delay, 0) || (country != "hk" && item.Delay > 80) {
				continue
			}
			name = "url." + c.Domain
		}
		key := dnsRecordKey(name, kind)
		groups[key] = append(groups[key], item)
	}
	result := map[string]dnsPlanRecord{}
	for key, items := range groups {
		sort.Slice(items, func(i, j int) bool {
			if items[i].Delay == items[j].Delay {
				return items[i].IP < items[j].IP
			}
			return items[i].Delay < items[j].Delay
		})
		parts := strings.Split(key, "/")
		r := dnsPlanRecord{Name: parts[0], Type: parts[1], Country: strings.ToUpper(strings.Split(parts[0], ".")[0]), Desired: []string{}, Current: []string{}}
		seen := map[string]bool{}
		for _, item := range items {
			if !seen[item.IP] && len(r.Desired) < c.MaxIPs {
				r.Desired = append(r.Desired, item.IP)
				seen[item.IP] = true
			}
		}
		sort.Strings(r.Desired)
		result[key] = r
	}
	return result
}
