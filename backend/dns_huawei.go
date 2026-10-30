package main

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/huaweicloud/huaweicloud-sdk-go-v3/core/auth/basic"
	"github.com/huaweicloud/huaweicloud-sdk-go-v3/core/config"
	dns "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/dns/v2"
	"github.com/huaweicloud/huaweicloud-sdk-go-v3/services/dns/v2/model"
	"github.com/huaweicloud/huaweicloud-sdk-go-v3/services/dns/v2/region"
)

type dnsRemoteRecord struct {
	ID, Name, Type, Line, Status string
	TTL                          int32
	Values                       []string
}

type dnsProvider interface {
	Zone(context.Context, string) (string, error)
	Records(context.Context, string) ([]dnsRemoteRecord, error)
	Apply(context.Context, string, dnsPlanRecord, int32) error
}

type huaweiDNS struct{ client *dns.DnsClient }

func dnsCredentialsReady() bool {
	return os.Getenv("HUAWEICLOUD_SDK_AK") != "" && os.Getenv("HUAWEICLOUD_SDK_SK") != ""
}
func newHuaweiDNS() (dnsProvider, error) {
	if !dnsCredentialsReady() {
		return nil, fmt.Errorf("请在 Go 服务配置 HUAWEICLOUD_SDK_AK 和 HUAWEICLOUD_SDK_SK 并重启")
	}
	r, err := region.SafeValueOf(env("HUAWEICLOUD_DNS_REGION", "cn-north-4"))
	if err != nil {
		return nil, fmt.Errorf("华为云 DNS 区域配置无效")
	}
	auth, err := basic.NewCredentialsBuilder().WithAk(os.Getenv("HUAWEICLOUD_SDK_AK")).WithSk(os.Getenv("HUAWEICLOUD_SDK_SK")).WithProjectId(os.Getenv("HUAWEICLOUD_PROJECT_ID")).SafeBuild()
	if err != nil {
		return nil, fmt.Errorf("华为云凭据配置无效")
	}
	client, err := dns.DnsClientBuilder().WithRegion(r).WithCredential(auth).WithHttpConfig(config.DefaultHttpConfig().WithTimeout(8 * time.Second).WithRetries(0)).SafeBuild()
	if err != nil {
		return nil, fmt.Errorf("无法初始化华为云 DNS 客户端")
	}
	return &huaweiDNS{client: dns.NewDnsClient(client)}, nil
}
func ptrValue[T any](p *T) (v T) {
	if p != nil {
		return *p
	}
	return
}

func (h *huaweiDNS) Zone(ctx context.Context, domain string) (string, error) {
	name := domain + "."
	equal := "equal"
	limit := int32(500)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	resp, err := h.client.ListPublicZones(&model.ListPublicZonesRequest{Name: &name, SearchMode: &equal, Limit: &limit})
	if err != nil {
		return "", fmt.Errorf("查询华为云托管域名失败：%w", err)
	}
	found := ""
	for _, z := range ptrValue(resp.Zones) {
		if strings.EqualFold(strings.TrimSuffix(ptrValue(z.Name), "."), domain) {
			if found != "" {
				return "", fmt.Errorf("存在多个同名托管域名")
			}
			found = ptrValue(z.Id)
			if ptrValue(z.Status) != "ACTIVE" {
				return "", fmt.Errorf("托管域名状态不是 ACTIVE")
			}
		}
	}
	if found == "" {
		return "", fmt.Errorf("华为云账号下未找到公网托管域名 %s", domain)
	}
	return found, nil
}
func (h *huaweiDNS) Records(ctx context.Context, zone string) ([]dnsRemoteRecord, error) {
	result := []dnsRemoteRecord{}
	limit := int32(500)
	offset := int32(0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := h.client.ListRecordSetsWithLine(&model.ListRecordSetsWithLineRequest{ZoneId: &zone, Limit: &limit, Offset: &offset})
		if err != nil {
			return nil, fmt.Errorf("查询华为云解析失败：%w", err)
		}
		items := ptrValue(resp.Recordsets)
		for _, r := range items {
			result = append(result, dnsRemoteRecord{ID: ptrValue(r.Id), Name: strings.ToLower(strings.TrimSuffix(ptrValue(r.Name), ".")), Type: ptrValue(r.Type), Line: ptrValue(r.Line), Status: ptrValue(r.Status), TTL: ptrValue(r.Ttl), Values: ptrValue(r.Records)})
		}
		if len(items) < int(limit) {
			return result, nil
		}
		offset += limit
		if offset > 100000 {
			return nil, fmt.Errorf("解析记录数量超过安全查询上限")
		}
	}
}
func (h *huaweiDNS) Apply(ctx context.Context, zone string, r dnsPlanRecord, ttl int32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch r.Action {
	case "create":
		line := "default_view"
		_, err := h.client.CreateRecordSetWithLine(&model.CreateRecordSetWithLineRequest{ZoneId: zone, Body: &model.CreateRecordSetWithLineRequestBody{Name: r.Name + ".", Type: r.Type, Ttl: &ttl, Records: &r.Desired, Line: &line}})
		return err
	case "update":
		_, err := h.client.UpdateRecordSets(&model.UpdateRecordSetsRequest{ZoneId: zone, RecordsetId: r.ID, Body: &model.UpdateRecordSetsReq{Name: r.Name + ".", Type: r.Type, Ttl: &ttl, Records: &r.Desired}})
		return err
	case "delete":
		_, err := h.client.DeleteRecordSets(&model.DeleteRecordSetsRequest{ZoneId: zone, RecordsetId: r.ID})
		return err
	}
	return nil
}

// Manage country names and the optional website name on the default line.
// Existing records are adopted; weighted duplicates or CNAME conflicts are blocked.
func dnsBuildPlan(c dnsConfig, candidates []dnsCandidate, remote []dnsRemoteRecord, managed map[string]bool, uncertain bool) []dnsPlanRecord {
	desired := dnsDesired(c, candidates)
	unknown := map[string]bool{}
	for _, ip := range candidates {
		if ip.State == "unknown" {
			unknown[ip.Pool+"/"+ip.IP] = true
		}
	}
	byKey := map[string][]dnsRemoteRecord{}
	cnames := map[string]bool{}
	for _, r := range remote {
		if r.Type == "CNAME" {
			cnames[r.Name] = true
		}
		if r.Line != "default_view" {
			continue
		}
		prefix, ok := strings.CutSuffix(r.Name, "."+c.Domain)
		if !ok || (dnsCountry(prefix) == "" && !(c.WebsiteEnabled && prefix == "url")) || (r.Type != "A" && r.Type != "AAAA") {
			continue
		}
		key := dnsRecordKey(r.Name, r.Type)
		byKey[key] = append(byKey[key], r)
		if _, ok := desired[key]; !ok {
			desired[key] = dnsPlanRecord{Name: r.Name, Type: r.Type, Country: strings.ToUpper(prefix), Desired: []string{}, Current: []string{}}
		}
	}
	result := []dnsPlanRecord{}
	for key, r := range desired {
		existing := byKey[key]
		if len(existing) > 0 {
			r.ID = existing[0].ID
			for _, old := range existing {
				r.Current = append(r.Current, old.Values...)
			}
			sort.Strings(r.Current)
		}
		// Preserve uncertain addresses already published, without allowing them
		// to block removal of confirmed failures from the same country's record.
		for _, ip := range r.Current {
			pool := ""
			if r.Name == "url."+c.Domain {
				pool = "cf"
			}
			if unknown[pool+"/"+ip] && !slices.Contains(r.Desired, ip) {
				r.Desired = append(r.Desired, ip)
				r.Reason = "保留已发布但采样待确认的 IP，待下一轮验证"
			}
		}
		sort.Strings(r.Desired)
		switch {
		case uncertain:
			r.Action = "hold"
			r.Reason = "探针采样或归属地不完整，保留原解析"
		case len(existing) > 1 || cnames[r.Name]:
			r.Action = "hold"
			r.Reason = "存在同线路多条权重记录或 CNAME 冲突，请先手动整理"
		case len(existing) > 0 && existing[0].Status != "ACTIVE":
			r.Action = "hold"
			r.Reason = "云端记录不是正常状态，暂不修改"
		case len(r.Desired) == 0:
			if len(existing) > 0 && managed[key] {
				r.Action = "delete"
				r.Reason = "已接管的记录没有符合条件的 IP"
			} else {
				r.Action = "hold"
				r.Reason = "没有健康 IP，尚未接管的记录保留"
			}
		case len(existing) == 0:
			r.Action = "create"
		case strings.Join(r.Current, ",") == strings.Join(r.Desired, ",") && existing[0].TTL == c.TTL:
			r.Action = "unchanged"
		default:
			r.Action = "update"
		}
		result = append(result, r)
	}
	sort.Slice(result, func(i, j int) bool {
		return dnsRecordKey(result[i].Name, result[i].Type) < dnsRecordKey(result[j].Name, result[j].Type)
	})
	return result
}

// Compare only this name/type on the managed line, plus conflicting CNAMEs.
func dnsSameRemote(before, after []dnsRemoteRecord, name, kind string) bool {
	snapshot := func(records []dnsRemoteRecord) []dnsRemoteRecord {
		result := []dnsRemoteRecord{}
		for _, r := range records {
			if r.Name != name || !(r.Type == "CNAME" || (r.Type == kind && r.Line == "default_view")) {
				continue
			}
			r.Values = append([]string{}, r.Values...)
			sort.Strings(r.Values)
			result = append(result, r)
		}
		sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
		return result
	}
	return reflect.DeepEqual(snapshot(before), snapshot(after))
}
