package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/common/convert"
	C "github.com/metacubex/mihomo/constant"
)

// Use mihomo's share-link converter and transport implementations; never rewrite
// the whole URI when testing an IP, since SNI and HTTP Host must stay unchanged.
func parseShare(raw string) (map[string]any, *url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Scheme != "vless" || u.User == nil || u.Hostname() == "" || len(raw) > 8192 || strings.ContainsAny(raw, "\r\n\t ") {
		return nil, nil, errors.New("请输入一条有效的 VLESS 链接")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || validate.Var(u.User.Username(), "uuid") != nil {
		return nil, nil, errors.New("VLESS 链接需包含有效的 UUID、服务器地址和端口")
	}
	if _, set := u.User.Password(); set || u.Path != "" {
		return nil, nil, errors.New("VLESS 链接格式无效")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, nil, errors.New("链接参数编码无效")
	}
	allowed := map[string]bool{"encryption": true, "security": true, "sni": true, "type": true, "path": true, "host": true, "mode": true, "alpn": true, "serviceName": true, "fp": true}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return nil, nil, fmt.Errorf("暂不支持或重复的链接参数：%s", key)
		}
	}
	if enc := q.Get("encryption"); enc != "" && enc != "none" {
		return nil, nil, errors.New("目前支持 encryption=none 的 VLESS 节点")
	}
	switch q.Get("security") {
	case "tls", "none", "":
	default:
		return nil, nil, errors.New("目前支持 TLS 或无 TLS 的 VLESS 节点")
	}
	switch q.Get("type") {
	case "httpupgrade", "xhttp", "grpc", "ws", "tcp", "":
	default:
		return nil, nil, errors.New("目前支持 HTTPUpgrade、XHTTP、gRPC、WS 和 TCP")
	}
	if q.Get("type") == "xhttp" {
		switch q.Get("mode") {
		case "", "auto", "packet-up", "stream-up", "stream-one":
		default:
			return nil, nil, errors.New("XHTTP mode 无效")
		}
	}
	// Set defaults before conversion so changing server never changes implicit SNI/Host.
	if q.Get("sni") == "" {
		sni := u.Hostname()
		if (q.Get("type") == "ws" || q.Get("type") == "httpupgrade") && q.Get("host") != "" {
			sni = q.Get("host")
		}
		q.Set("sni", sni)
	}
	if q.Get("host") == "" {
		host := u.Hostname()
		if q.Get("type") == "xhttp" || q.Get("type") == "grpc" {
			host = q.Get("sni")
		}
		q.Set("host", host)
	}
	if q.Get("type") == "grpc" && q.Get("host") != q.Get("sni") {
		return nil, nil, errors.New("当前内核的 gRPC Host 需与 SNI 一致")
	}
	normalized := *u
	normalized.RawQuery = q.Encode()
	configs, err := convert.ConvertsV2Ray([]byte(normalized.String()))
	if err != nil || len(configs) != 1 {
		return nil, nil, errors.New("无法解析该 VLESS 链接，请检查参数")
	}
	config := configs[0]
	// v1.19.33's URI converter leaves network=httpupgrade, while the outbound
	// implements HTTPUpgrade via the WS transport flag. Normalize that mapping.
	if q.Get("type") == "httpupgrade" {
		config["network"] = "ws"
		opts, ok := config["ws-opts"].(map[string]any)
		if !ok {
			return nil, nil, errors.New("HTTPUpgrade 配置无效")
		}
		opts["v2ray-http-upgrade"] = true
	}
	config["name"] = "ipguard-probe" // Keep credentials and user labels out of kernel logs.
	if env("PROBE_KERNEL", "mihomo") == "xray" {
		return config, &normalized, nil
	}
	proxy, err := adapter.ParseProxy(config)
	if err != nil {
		return nil, nil, errors.New("mihomo 无法加载此节点，请检查传输参数")
	}
	_ = proxy.Close()
	return config, &normalized, nil
}

func expandPrefixes(cidrs []string) ([]netip.Addr, error) {
	seen := map[netip.Addr]bool{}
	ips := []netip.Addr{}
	for _, cidr := range cidrs {
		p, err := netip.ParsePrefix(cidr)
		if err != nil || p != p.Masked() || p.Addr().Is4In6() {
			return nil, errors.New("网段无效")
		}
		if p.Addr().BitLen()-p.Bits() > 12 {
			return nil, errors.New("单次最多测试 4096 个 IP，请将大网段拆分后选择")
		}
		for ip := p.Addr(); ip.IsValid() && p.Contains(ip); ip = ip.Next() {
			if !seen[ip] {
				seen[ip] = true
				ips = append(ips, ip)
			}
			if len(ips) > 4096 {
				return nil, errors.New("所选网段去重后超过 4096 个 IP，请减少选择")
			}
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("请选择至少一个网段")
	}
	return ips, nil
}

// The template is parsed once per job and remains immutable. Mihomo decodes
// nested options into fresh structs/maps; only the top-level server is replaced.
func newProbe(template map[string]any, ip netip.Addr) (C.Proxy, error) {
	config := maps.Clone(template)
	config["server"] = ip.String()
	if iface := env("PROBE_INTERFACE", ""); iface != "" {
		config["interface-name"] = iface
	}
	return adapter.ParseProxy(config)
}

func probeIP(ctx context.Context, template map[string]any, ip netip.Addr, target string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if env("PROBE_KERNEL", "mihomo") == "xray" {
		return probeXray(ctx, template, ip, target)
	}
	proxy, err := newProbe(template, ip)
	if err != nil {
		return 0, errors.New("节点配置加载失败")
	}
	defer proxy.Close()
	delay, err := proxy.URLTest(ctx, target, nil)
	if err != nil {
		return 0, err
	}
	return int(delay), nil
}
