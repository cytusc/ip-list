package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"
)

// Leave headroom below Feishu's 30 KB message limit, including JSON escaping.
const dnsFeishuCardLimit = 20 * 1024
const dnsFeishuRecordsPerCard = 10

type dnsFeishuMessage struct {
	MsgType string        `json:"msg_type"`
	Card    dnsFeishuCard `json:"card"`
}

type dnsFeishuCard struct {
	Config struct {
		WideScreenMode bool `json:"wide_screen_mode"`
	} `json:"config"`
	Header struct {
		Template string            `json:"template"`
		Title    map[string]string `json:"title"`
	} `json:"header"`
	Elements []map[string]any `json:"elements"`
}

// Compare IP sets, so ordering, duplicate values and IPv6 spelling changes
// never produce false notifications. DNS TTL-only updates are silent too.
func dnsIPDifference(values, other []string) []string {
	addresses := func(items []string) map[netip.Addr]bool {
		result := map[netip.Addr]bool{}
		for _, value := range items {
			if ip, err := netip.ParseAddr(value); err == nil {
				result[ip.Unmap()] = true
			}
		}
		return result
	}
	before, after := addresses(values), addresses(other)
	result := []string{}
	for ip := range before {
		if !after[ip] {
			result = append(result, ip.String())
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return netip.MustParseAddr(result[i]).Less(netip.MustParseAddr(result[j]))
	})
	return result
}

func dnsFeishuMarkdown(content string) map[string]any {
	return map[string]any{"tag": "div", "text": map[string]string{"tag": "lark_md", "content": content}}
}

func dnsFeishuIPList(label string, ips []string, delays map[string]float64, providers map[string]string) map[string]any {
	content := "无"
	if len(ips) > 0 {
		lines := make([]string, 0, len(ips))
		for _, ip := range ips {
			line := "`" + ip + "`"
			if delays != nil {
				if delay, ok := delays[ip]; ok {
					line += fmt.Sprintf(" · 探针平均延迟 %.1f ms", delay)
				} else {
					line += " · 探针平均延迟暂无"
				}
			}
			if providers != nil {
				provider := strings.TrimSpace(providers[ip])
				if provider == "" {
					provider = "未知运营商"
				}
				line += " · 运营商：" + provider
			}
			lines = append(lines, line)
		}
		content = strings.Join(lines, "\n")
	}
	return dnsFeishuMarkdown(fmt.Sprintf("**%s（%d）**\n%s", label, len(ips), content))
}

func dnsFeishuMessages(report dnsReport) ([]dnsFeishuMessage, error) {
	if report.Mode != "sync" {
		return nil, nil
	}
	at := report.StartedAt
	if report.FinishedAt != nil {
		at = *report.FinishedAt
	}
	newMessage := func() dnsFeishuMessage {
		m := dnsFeishuMessage{MsgType: "interactive"}
		m.Card.Config.WideScreenMode = true
		m.Card.Header.Template = "blue"
		m.Card.Header.Title = map[string]string{"tag": "plain_text", "content": "🌐 DNS IP 变动通知"}
		m.Card.Elements = []map[string]any{dnsFeishuMarkdown(fmt.Sprintf("**托管域名**：%s\n**同步时间**：%s（北京时间）", report.Domain, at.In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02 15:04:05")))}
		if report.Error != "" {
			m.Card.Elements = append(m.Card.Elements, dnsFeishuMarkdown("⚠️ 本轮部分同步失败，以下仅列出已成功提交的 IP 变更。"))
		}
		return m
	}
	finish := func(m dnsFeishuMessage) dnsFeishuMessage {
		m.Card.Elements = append(m.Card.Elements, map[string]any{"tag": "note", "elements": []map[string]string{{"tag": "plain_text", "content": "已提交华为云；实际生效时间取决于云端处理及 DNS 缓存。"}}})
		return m
	}
	var messages []dnsFeishuMessage
	m := newMessage()
	count := 0
	delays := make(map[string]float64, len(report.Candidates))
	providers := make(map[string]string, len(report.Candidates))
	for _, candidate := range report.Candidates {
		ip, err := netip.ParseAddr(candidate.IP)
		if err != nil {
			continue
		}
		key := ip.Unmap().String()
		if provider := strings.TrimSpace(candidate.Provider); provider != "" {
			providers[key] = provider
		}
		if candidate.State != "healthy" || candidate.Delay < 0 || math.IsNaN(candidate.Delay) || math.IsInf(candidate.Delay, 0) {
			continue
		}
		if old, ok := delays[key]; !ok || candidate.Delay < old {
			delays[key] = candidate.Delay
		}
	}
	records := append([]dnsPlanRecord(nil), report.Records...)
	sort.Slice(records, func(i, j int) bool {
		return dnsRecordKey(records[i].Name, records[i].Type) < dnsRecordKey(records[j].Name, records[j].Type)
	})
	for _, r := range records {
		prefix, ok := strings.CutSuffix(r.Name, "."+report.Domain)
		country := dnsCountry(prefix)
		if !r.Applied || !ok || country == "" || (r.Type != "A" && r.Type != "AAAA") || (r.Action != "create" && r.Action != "update" && r.Action != "delete") {
			continue
		}
		added, removed := dnsIPDifference(r.Desired, r.Current), dnsIPDifference(r.Current, r.Desired)
		if len(added)+len(removed) == 0 {
			continue
		}
		action := map[string]string{"create": "新增解析", "update": "更新解析", "delete": "删除解析"}[r.Action]
		flag := string([]rune{0x1F1E6 + rune(country[0]-'a'), 0x1F1E6 + rune(country[1]-'a')})
		block := []map[string]any{
			{"tag": "hr"},
			dnsFeishuMarkdown(fmt.Sprintf("**%s %s · %s**\n%s · %s · 新增 **%d** / 移除 **%d**", flag, strings.ToUpper(country), r.Name, r.Type, action, len(added), len(removed))),
			dnsFeishuIPList("🟢 新增 IP", added, delays, providers),
			dnsFeishuIPList("🔴 移除 IP", removed, nil, nil),
		}
		candidate := m
		candidate.Card.Elements = append(append([]map[string]any(nil), m.Card.Elements...), block...)
		raw, err := json.Marshal(finish(candidate))
		if err != nil {
			return nil, err
		}
		if len(raw) > dnsFeishuCardLimit || count >= dnsFeishuRecordsPerCard {
			if count == 0 {
				return nil, fmt.Errorf("单条 DNS 变更超过飞书卡片大小限制")
			}
			messages = append(messages, finish(m))
			m = newMessage()
			m.Card.Elements = append(m.Card.Elements, block...)
			raw, err = json.Marshal(finish(m))
			if err != nil {
				return nil, err
			}
			if len(raw) > dnsFeishuCardLimit {
				return nil, fmt.Errorf("单条 DNS 变更超过飞书卡片大小限制")
			}
			count = 1
		} else {
			m = candidate
			count++
		}
	}
	if count > 0 {
		messages = append(messages, finish(m))
	}
	return messages, nil
}

func sendDNSFeishu(ctx context.Context, client *http.Client, webhook string, messages []dnsFeishuMessage) error {
	for i, message := range messages {
		// Stay below the bot's per-second rate limit when a large run is split.
		if i > 0 {
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("飞书通知发送超时或已取消")
			case <-timer.C:
			}
		}
		raw, err := json.Marshal(message)
		if err != nil {
			return fmt.Errorf("飞书卡片编码失败")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("飞书 Webhook 地址无效")
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			// HTTP errors include the request URL; never log the webhook token.
			return fmt.Errorf("飞书通知请求失败，请检查网络和 Webhook 配置")
		}
		var result struct {
			Code       *int `json:"code"`
			StatusCode *int `json:"StatusCode"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&result)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("飞书通知返回 HTTP %d（第 %d/%d 张卡片）", resp.StatusCode, i+1, len(messages))
		}
		if err != nil || (result.Code == nil && result.StatusCode == nil) {
			return fmt.Errorf("飞书通知响应格式无效")
		}
		if result.Code != nil && *result.Code != 0 {
			return fmt.Errorf("飞书通知失败，错误码 %d（第 %d/%d 张卡片）", *result.Code, i+1, len(messages))
		}
		if result.StatusCode != nil && *result.StatusCode != 0 {
			return fmt.Errorf("飞书通知失败，错误码 %d", *result.StatusCode)
		}
	}
	return nil
}

func notifyDNSReport(report *dnsReport) {
	webhook := strings.TrimSpace(os.Getenv("FEISHU_DNS_WEBHOOK_URL"))
	if webhook == "" {
		return
	}
	messages, err := dnsFeishuMessages(*report)
	if err == nil && len(messages) > 0 {
		// Cloud writes may have succeeded even if the run was canceled or only
		// partially completed. Give their notification its own bounded context.
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err = sendDNSFeishu(ctx, &http.Client{Timeout: 5 * time.Second}, webhook, messages)
	}
	if err != nil {
		warning := "DNS 变更飞书通知未全部送达：" + err.Error()
		if report.Warning != "" {
			report.Warning += "；"
		}
		report.Warning += warning
		log.Print(warning)
	}
}
