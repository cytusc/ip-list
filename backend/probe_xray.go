package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Each probe gets a private Unix socket and process; no public listener or
// shared routing state is changed. The scan deadline covers startup and HTTP.
func probeXray(ctx context.Context, template map[string]any, ip netip.Addr, target string) (int, error) {
	port, err := strconv.Atoi(fmt.Sprint(template["port"]))
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("Xray 节点端口无效")
	}
	dir, err := os.MkdirTemp("", "ipguard-xray-")
	if err != nil {
		return 0, errors.New("Xray 临时目录创建失败")
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "proxy.sock")
	stream := map[string]any{"network": template["network"]}
	switch template["network"] {
	case "grpc":
		opts, _ := template["grpc-opts"].(map[string]any)
		stream["grpcSettings"] = map[string]any{"serviceName": opts["grpc-service-name"]}
	case "xhttp":
		stream["xhttpSettings"] = template["xhttp-opts"]
	case "ws":
		opts, _ := template["ws-opts"].(map[string]any)
		if upgrade, _ := opts["v2ray-http-upgrade"].(bool); upgrade {
			headers, _ := opts["headers"].(map[string]any)
			stream["network"] = "httpupgrade"
			stream["httpupgradeSettings"] = map[string]any{"path": opts["path"], "host": headers["Host"]}
		} else {
			stream["wsSettings"] = map[string]any{"path": opts["path"], "headers": opts["headers"]}
		}
	case "tcp", "":
		stream["network"] = "tcp"
	default:
		return 0, errors.New("Xray 不支持此传输方式")
	}
	if tls, _ := template["tls"].(bool); tls {
		stream["security"] = "tls"
		stream["tlsSettings"] = map[string]any{
			"serverName": template["servername"], "alpn": template["alpn"],
			"allowInsecure": false, "fingerprint": template["client-fingerprint"],
		}
	}
	if iface := env("PROBE_INTERFACE", ""); iface != "" {
		stream["sockopt"] = map[string]any{"interface": iface}
	}
	config := map[string]any{
		"log": map[string]any{"loglevel": "none"},
		"inbounds": []any{map[string]any{
			"listen": socket, "protocol": "http", "settings": map[string]any{},
		}},
		"outbounds": []any{xrayOutbound(template, ip, port, stream)},
	}
	if env("PROBE_DEBUG", "") == "1" {
		config["log"] = map[string]any{"loglevel": "info"}
	}
	data, err := json.Marshal(config)
	if err != nil {
		return 0, errors.New("Xray 节点配置生成失败")
	}
	cmd := exec.CommandContext(ctx, env("XRAY_BIN", "/usr/local/bin/xray"), "run", "-config", "stdin:")
	cmd.Stdin = bytes.NewReader(data)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return 0, errors.New("Xray 启动失败，请检查 XRAY_BIN")
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	defer func() { _ = cmd.Process.Kill(); <-done }()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-done:
			detail := strings.ReplaceAll(strings.ReplaceAll(output.String(), fmt.Sprint(template["uuid"]), "[credential]"), fmt.Sprint(template["password"]), "[credential]")
			return 0, fmt.Errorf("Xray 配置加载失败: %s", strings.TrimSpace(detail))
		case <-ticker.C:
		}
	}
	transport := &http.Transport{
		Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "xray.local"}),
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	response, err := client.Do(req)
	if err != nil {
		_ = cmd.Process.Kill()
		<-done
		if env("PROBE_DEBUG", "") == "1" {
			detail := strings.ReplaceAll(strings.ReplaceAll(output.String(), fmt.Sprint(template["uuid"]), "[credential]"), fmt.Sprint(template["password"]), "[credential]")
			detail = strings.ReplaceAll(detail, target, "[test-url]")
			log.Printf("xray probe ip=%s details=%q", ip, detail)
		}
		return 0, err
	}
	_ = response.Body.Close()
	return max(1, int(time.Since(start).Milliseconds())), nil
}

func xrayOutbound(template map[string]any, ip netip.Addr, port int, stream map[string]any) map[string]any {
	protocol := "vless"
	settings := map[string]any{"vnext": []any{map[string]any{
		"address": ip.String(), "port": port,
		"users": []any{map[string]any{"id": template["uuid"], "encryption": "none"}},
	}}}
	if template["type"] == "trojan" {
		protocol = "trojan"
		settings = map[string]any{"servers": []any{map[string]any{"address": ip.String(), "port": port, "password": template["password"]}}}
	}
	return map[string]any{"protocol": protocol, "streamSettings": stream, "settings": settings}
}
