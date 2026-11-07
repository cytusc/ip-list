package main

import (
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/text/language"
)

type dnsConfig struct {
	WebsiteEnabled bool   `json:"website_enabled"`
	WebsiteURL     string `json:"website_url"`
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
	c.WebsiteURL = strings.TrimSpace(c.WebsiteURL)
	if c.WebsiteEnabled || c.WebsiteURL != "" {
		u, err := url.Parse(c.WebsiteURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(c.WebsiteURL) > 2048 {
			return fmt.Errorf("网站优选需填写实际业务的 HTTPS 检测地址，例如 https://api.example.com/，不含账号或片段")
		}
	}
	c.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(c.Domain), "."))
	if len(c.Domain) > 253 || !regexp.MustCompile(`^([a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`).MatchString(c.Domain) {
		return fmt.Errorf("请输入有效的托管域名，例如 reiiv.com")
	}
	if c.Interval < 60 || c.Interval > 86400 || c.TTL < 60 || c.TTL > 86400 || c.MaxIPs < 1 || c.MaxIPs > 50 || c.Window < 2 || c.Window > 60 || c.MinSuccess < 1 || c.MinSuccess > 100 {
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

const dnsContinuousFailureWindow = 5 * time.Minute

// Heartbeats do not prove that the probe's outbound network works. A failed
// target is actionable only if the same probe can still reach another target.
func dnsCorroborateFailure(state string, otherTargetOK bool) string {
	if (state == "failed" || state == "pending") && !otherTargetOK {
		return "unknown"
	}
	return state
}

// Loss counts before latency trimming; continuous failures remain a stronger signal.
func dnsProbeHealth(samples []dnsSample, now time.Time, freshness time.Duration, c dnsConfig) (string, float64) {
	cutoff := now.Add(-time.Duration(c.Window) * time.Minute)
	samples = append([]dnsSample(nil), samples...)
	sort.Slice(samples, func(i, j int) bool { return samples[i].At.Before(samples[j].At) })
	valid := make([]dnsSample, 0, len(samples))
	for _, s := range samples {
		if !s.At.Before(cutoff) && !s.At.After(now) {
			valid = append(valid, s)
		}
	}
	if len(valid) < 2 || now.Sub(valid[len(valid)-1].At) > freshness || valid[len(valid)-1].At.Sub(valid[0].At) < time.Minute {
		return "unknown", 0
	}
	latest := valid[len(valid)-1]
	if !latest.Success || latest.Delay < 0 || math.IsNaN(latest.Delay) || math.IsInf(latest.Delay, 0) {
		failureStart := latest.At
		for i := len(samples) - 2; i >= 0; i-- {
			if samples[i].At.After(latest.At) {
				continue
			}
			if (samples[i].Success && samples[i].Delay >= 0 && !math.IsNaN(samples[i].Delay) && !math.IsInf(samples[i].Delay, 0)) || failureStart.Sub(samples[i].At) > freshness {
				break
			}
			failureStart = samples[i].At
		}
		if latest.At.Sub(failureStart) >= dnsContinuousFailureWindow {
			return "failed", 0
		}
	}
	for i := 1; i < len(valid); i++ {
		if valid[i].At.Sub(valid[i-1].At) > freshness {
			return "unknown", 0
		}
	}
	successes := 0
	for _, sample := range valid {
		if sample.Success && sample.Delay >= 0 && !math.IsNaN(sample.Delay) && !math.IsInf(sample.Delay, 0) {
			successes++
		}
	}
	if successes*100 < max(c.MinSuccess, 1)*len(valid) {
		return "pending", 0
	}
	delay, ok := dnsP95Mean(valid)
	if !ok {
		return "unknown", 0
	}
	return "healthy", delay
}

type nodeHealthAttempt struct {
	Protocol    string            `json:"protocol"`
	Certificate *certificateCheck `json:"certificate,omitempty"`
	Error       string            `json:"error,omitempty"`
}

type dnsCandidate struct {
	HealthAttempts []nodeHealthAttempt `json:"health_attempts,omitempty"`
	HealthControl  bool                `json:"-"`
	HTTPSAttempts  []string            `json:"https_attempts,omitempty"`
	HTTPSState     string              `json:"https_state,omitempty"`
	HTTPSURL       string              `json:"https_url,omitempty"`
	HTTPSReason    string              `json:"https_reason,omitempty"`
	Pool           string              `json:"pool,omitempty"`
	IP             string              `json:"ip"`
	Provider       string              `json:"provider"`
	Country        string              `json:"country"`
	State          string              `json:"state"`
	Reason         string              `json:"reason"`
	Delay          float64             `json:"delay_ms"`
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
			if c.WebsiteURL != "" && (item.HTTPSState != "healthy" || item.HTTPSURL != c.WebsiteURL) {
				continue
			}
			if !c.WebsiteEnabled || (country != "hk" && country != "jp" && country != "sg") || item.Delay < 0 || math.IsNaN(item.Delay) || math.IsInf(item.Delay, 0) {
				continue
			}
			name = "url." + c.Domain
		}
		key := dnsRecordKey(name, kind)
		groups[key] = append(groups[key], item)
	}
	// Fall back independently for A and AAAA, using only healthy Hong Kong
	// candidates from the ordinary protocol pool. Website candidates must not
	// bypass their separate admission policy or enter country records.
	fallback := map[string]bool{}
	for _, kind := range []string{"A", "AAAA"} {
		key := dnsRecordKey("sg."+c.Domain, kind)
		if len(groups[key]) != 0 {
			continue
		}
		for _, item := range groups[dnsRecordKey("hk."+c.Domain, kind)] {
			if item.Delay >= 0 && !math.IsNaN(item.Delay) && !math.IsInf(item.Delay, 0) {
				groups[key] = append(groups[key], item)
			}
		}
		fallback[key] = len(groups[key]) > 0
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
		limit := c.MaxIPs
		if fallback[key] {
			limit = min(limit, 3)
			r.Reason = "新加坡没有符合条件的 IP，使用香港延迟最低的最多 3 个 IP 兜底"
		}
		seen := map[string]bool{}
		for _, item := range items {
			if !seen[item.IP] && len(r.Desired) < limit {
				r.Desired = append(r.Desired, item.IP)
				seen[item.IP] = true
			}
		}
		sort.Strings(r.Desired)
		result[key] = r
	}
	return result
}
