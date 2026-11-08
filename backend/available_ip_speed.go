package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

const availableIPSpeedTestBytes = 5 << 20
const availableIPAutomaticSpeedTestBytes = 100_000_000
const availableIPSpeedTestChunkBytes = 50_000_000
const availableIPSpeedTestURL = "https://speed.cloudflare.com/__down"

type availableIPSpeedResult struct {
	Duration  int64   `json:"duration_ms"`
	SpeedMiBs float64 `json:"speed_mib_s"`
}

type availableIPSpeedTest struct {
	Duration  int64     `json:"duration_ms"`
	SpeedMiBs float64   `json:"speed_mib_s"`
	CheckedAt time.Time `json:"checked_at"`
	Success   bool      `json:"success"`
}

func (a *app) speedTestAvailableIP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IP string `json:"ip"`
	}
	if !decode(w, r, &input) {
		return
	}
	ip, err := netip.ParseAddr(input.IP)
	if err != nil || ip.Zone() != "" {
		apiError(w, 422, "请输入有效的 IP 地址", nil)
		return
	}
	var available bool
	if err = a.db.QueryRow(r.Context(), availableIPCandidatesSQL+` SELECT EXISTS(SELECT 1 FROM candidates WHERE ip=$5::inet)`, "", "", "", "", ip.String()).Scan(&available); err != nil {
		dbError(w, err)
		return
	}
	if !available {
		apiError(w, 409, "该 IP 已不在可用列表中，请刷新后重试", nil)
		return
	}
	var raw string
	if err = a.db.QueryRow(r.Context(), `SELECT share_uri FROM nodes WHERE name='HK2-gRPC' AND enabled AND share_uri<>'' ORDER BY id LIMIT 1`).Scan(&raw); err != nil {
		apiError(w, 503, "未找到启用的 HK2-gRPC 节点", nil)
		return
	}
	template, _, err := parseShare(raw)
	if err != nil || template["network"] != "grpc" {
		apiError(w, 503, "HK2-gRPC 节点配置无效", nil)
		return
	}
	result, err := downloadSpeedIP(r.Context(), template, ip, availableIPSpeedTestBytes)
	if err != nil {
		apiError(w, 502, "测速失败：HK2-gRPC 入口无法完成下载", nil)
		return
	}
	respond(w, http.StatusOK, result)
}

func downloadSpeedIP(ctx context.Context, template map[string]any, ip netip.Addr, bytes int64) (availableIPSpeedResult, error) {
	proxy, err := newProbe(template, ip)
	if err != nil {
		return availableIPSpeedResult{}, err
	}
	defer proxy.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = proxy.Close() })
	defer stopClose()
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			metadata := &C.Metadata{NetWork: C.TCP}
			if err := metadata.SetRemoteAddress(address); err != nil {
				return nil, err
			}
			return proxy.Adapter().DialContext(ctx, metadata)
		},
	}
	defer transport.CloseIdleConnections()
	target, err := url.Parse(availableIPSpeedTestURL)
	if err != nil {
		return availableIPSpeedResult{}, err
	}
	started := time.Now()
	client := &http.Client{Transport: transport}
	var downloaded int64
	for downloaded < bytes {
		chunk := min(availableIPSpeedTestChunkBytes, bytes-downloaded)
		query := target.Query()
		query.Set("bytes", fmt.Sprint(chunk))
		target.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return availableIPSpeedResult{}, err
		}
		response, err := client.Do(request)
		if err != nil {
			return availableIPSpeedResult{}, err
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			return availableIPSpeedResult{}, fmt.Errorf("download server returned HTTP %d", response.StatusCode)
		}
		count, copyErr := io.CopyN(io.Discard, response.Body, chunk)
		_ = response.Body.Close()
		if copyErr != nil {
			return availableIPSpeedResult{}, copyErr
		}
		downloaded += count
	}
	duration := time.Since(started)
	if duration < time.Millisecond {
		duration = time.Millisecond
	}
	return availableIPSpeedResult{Duration: duration.Milliseconds(), SpeedMiBs: float64(downloaded) / (1 << 20) / duration.Seconds()}, nil
}

func (a *app) maintainAvailableIPSpeedTests(ctx context.Context) {
	ticker := time.NewTicker(4 * time.Hour)
	defer ticker.Stop()
	for {
		a.runAvailableIPSpeedTests(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *app) runAvailableIPSpeedTests(ctx context.Context) {
	rows, err := a.db.Query(ctx, `SELECT DISTINCT host(p.ip) FROM shared_node_ip_pool p
 WHERE p.available AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=p.ip) ORDER BY host(p.ip)`)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("读取可用 IP 测速列表失败: %v", err)
		}
		return
	}
	var ips []netip.Addr
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			break
		}
		var ip netip.Addr
		ip, err = netip.ParseAddr(raw)
		if err != nil {
			break
		}
		ips = append(ips, ip)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("解析可用 IP 测速列表失败: %v", err)
		}
		return
	}
	if _, err = a.db.Exec(ctx, `DELETE FROM available_ip_speed_tests s WHERE
 NOT EXISTS(SELECT 1 FROM shared_node_ip_pool p WHERE p.ip=s.ip AND p.available)
 OR EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=s.ip)`); err != nil {
		log.Printf("清理过期 IP 测速结果失败: %v", err)
	}
	if len(ips) == 0 {
		return
	}
	var raw string
	if err = a.db.QueryRow(ctx, `SELECT share_uri FROM nodes WHERE name='HK2-gRPC' AND enabled AND share_uri<>'' ORDER BY id LIMIT 1`).Scan(&raw); err != nil {
		log.Printf("定时 IP 测速未找到启用的 HK2-gRPC 节点: %v", err)
		a.saveAvailableIPSpeedFailures(ctx, ips)
		return
	}
	template, _, err := parseShare(raw)
	if err != nil || template["network"] != "grpc" {
		log.Print("定时 IP 测速的 HK2-gRPC 节点配置无效")
		a.saveAvailableIPSpeedFailures(ctx, ips)
		return
	}
	for i, ip := range ips {
		testCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		result, testErr := downloadSpeedIP(testCtx, template, ip, availableIPAutomaticSpeedTestBytes)
		cancel()
		if saveErr := a.saveAvailableIPSpeedResult(ctx, ip, result, testErr == nil); saveErr != nil && ctx.Err() == nil {
			log.Printf("保存 IP 测速结果失败 ip=%s: %v", ip, saveErr)
		}
		if testErr != nil && ctx.Err() == nil {
			log.Printf("IP 下载测速失败 ip=%s: %v", ip, testErr)
		}
		if i < len(ips)-1 {
			timer := time.NewTimer(3 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func (a *app) saveAvailableIPSpeedFailures(ctx context.Context, ips []netip.Addr) {
	for _, ip := range ips {
		if err := a.saveAvailableIPSpeedResult(ctx, ip, availableIPSpeedResult{}, false); err != nil && ctx.Err() == nil {
			log.Printf("保存 IP 测速失败状态失败 ip=%s: %v", ip, err)
		}
	}
}

func (a *app) saveAvailableIPSpeedResult(ctx context.Context, ip netip.Addr, result availableIPSpeedResult, success bool) error {
	_, err := a.db.Exec(ctx, `INSERT INTO available_ip_speed_tests(ip,duration_ms,speed_mib_s,checked_at,success)
 SELECT $1::inet,$2,$3,now(),$4 WHERE EXISTS(
 SELECT 1 FROM shared_node_ip_pool p WHERE p.ip=$1::inet AND p.available)
 AND NOT EXISTS(SELECT 1 FROM ip_blacklist b WHERE b.ip=$1::inet)
 ON CONFLICT(ip) DO UPDATE SET duration_ms=excluded.duration_ms,speed_mib_s=excluded.speed_mib_s,
 checked_at=excluded.checked_at,success=excluded.success`, ip.String(), result.Duration, result.SpeedMiBs, success)
	return err
}
