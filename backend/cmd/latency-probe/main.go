package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

var pingDelay = regexp.MustCompile(`(?i)(?:time|时间)[=<]\s*([0-9]+(?:\.[0-9]+)?)\s*ms`)

type config struct {
	Server string
	Token  string
	TLSPin string
}

type settings struct {
	IPs           []string `json:"ips"`
	Interval      int      `json:"interval_seconds"`
	TimeoutMS     int      `json:"timeout_ms"`
	Concurrency   int      `json:"concurrency"`
	InterfaceName string   `json:"interface_name"`
}

type result struct {
	IP        string    `json:"ip"`
	DelayMS   float64   `json:"delay_ms"`
	Success   bool      `json:"success"`
	CheckedAt time.Time `json:"checked_at"`
}

func main() {
	server := flag.String("server", os.Getenv("PROBE_SERVER"), "网站地址，例如 https://example.com")
	token := flag.String("token", os.Getenv("PROBE_TOKEN"), "探针密钥")
	configPath := flag.String("config", "", "JSON 配置文件")
	tlsPin := flag.String("tls-pin", "", "服务器公钥 SHA-256（base64）")
	once := flag.Bool("once", false, "执行一轮检测并上报后退出")
	check := flag.Bool("check", false, "检查连接和鉴权后退出")
	flag.Parse()
	if *configPath != "" {
		content, err := os.ReadFile(*configPath)
		if err != nil {
			log.Fatal(err)
		}
		var cfg config
		if err = json.Unmarshal(content, &cfg); err != nil {
			log.Fatal(err)
		}
		*server, *token, *tlsPin = cfg.Server, cfg.Token, cfg.TLSPin
	}
	if *server == "" || *token == "" {
		log.Fatal("请设置 -server 和 -token，或设置 PROBE_SERVER、PROBE_TOKEN")
	}
	base := strings.TrimRight(*server, "/")
	client, err := newHTTPClient(*tlsPin)
	if err != nil {
		log.Fatal(err)
	}
	if *check {
		var cfg settings
		for attempt := 0; attempt < 3; attempt++ {
			cfg, err = loadSettings(client, base, *token)
			if err == nil {
				break
			}
			if attempt < 2 {
				time.Sleep(2 * time.Second)
			}
		}
		if err != nil {
			log.Fatal(err)
		}
		if _, err = exec.LookPath("ping"); err != nil {
			log.Fatal("未找到系统 ping，请先安装 iputils-ping")
		}
		log.Printf("连接和鉴权成功：%d 个目标，间隔 %d 秒", len(cfg.IPs), cfg.Interval)
		return
	}
	for {
		settings, err := loadSettings(client, base, *token)
		if err != nil {
			if *once {
				log.Fatal(err)
			}
			log.Printf("读取探针任务失败: %v", err)
			time.Sleep(30 * time.Second)
			continue
		}
		results := runPings(settings)
		if len(results) > 0 {
			reportErr := error(nil)
			for start := 0; start < len(results); start += 5000 {
				end := min(start+5000, len(results))
				if err = report(client, base, *token, results[start:end]); err != nil {
					reportErr = err
					break
				}
			}
			if reportErr != nil {
				if *once {
					log.Fatal(reportErr)
				}
				log.Printf("上报检测结果失败: %v", reportErr)
			} else {
				ok := 0
				for _, item := range results {
					if item.Success {
						ok++
					}
				}
				log.Printf("已检测 %d 个 IP：%d 可达，%d 超时", len(results), ok, len(results)-ok)
			}
		}
		if *once {
			return
		}
		wait := time.Duration(settings.Interval) * time.Second
		if wait < 15*time.Second {
			wait = 15 * time.Second
		}
		time.Sleep(wait)
	}
}

func newHTTPClient(tlsPin string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if tlsPin != "" {
		expected, err := base64.StdEncoding.DecodeString(tlsPin)
		if err != nil || len(expected) != sha256.Size {
			return nil, fmt.Errorf("服务器公钥指纹格式无效")
		}
		transport.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			// A pinned public key replaces CA validation for private/self-signed sites.
			InsecureSkipVerify: true,
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return fmt.Errorf("服务器未提供证书")
				}
				actual := sha256.Sum256(cs.PeerCertificates[0].RawSubjectPublicKeyInfo)
				if !bytes.Equal(expected, actual[:]) {
					return fmt.Errorf("服务器公钥指纹不匹配")
				}
				return nil
			},
		}
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

func loadSettings(client *http.Client, base, token string) (settings, error) {
	var value settings
	err := requestJSON(client, http.MethodGet, base+"/api/probe-agent/targets", token, nil, &value)
	return value, err
}

func report(client *http.Client, base, token string, results []result) error {
	return requestJSON(client, http.MethodPost, base+"/api/probe-agent/results", token, map[string]any{"results": results}, nil)
}

func requestJSON(client *http.Client, method, url, token string, body, output any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		content, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(content)))
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(output)
	}
	_, err = io.Copy(io.Discard, response.Body)
	return err
}

func runPings(settings settings) []result {
	concurrency := settings.Concurrency
	if concurrency < 1 {
		concurrency = 64
	}
	if concurrency > 512 {
		concurrency = 512
	}
	timeout := time.Duration(settings.TimeoutMS) * time.Millisecond
	if timeout < 250*time.Millisecond {
		timeout = time.Second
	}
	jobs := make(chan string)
	results := make(chan result, len(settings.IPs))
	workers := concurrency
	if workers > len(settings.IPs) {
		workers = len(settings.IPs)
	}
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for rawIP := range jobs {
				ip, err := netip.ParseAddr(rawIP)
				if err != nil || ip.Zone() != "" {
					continue
				}
				started := time.Now().UTC()
				delay, success := ping(ip, timeout, settings.InterfaceName)
				results <- result{IP: ip.Unmap().String(), DelayMS: delay, Success: success, CheckedAt: started}
			}
		}()
	}
	go func() {
		for _, ip := range settings.IPs {
			jobs <- ip
		}
		close(jobs)
		group.Wait()
		close(results)
	}()
	items := make([]result, 0, len(settings.IPs))
	for item := range results {
		items = append(items, item)
	}
	return items
}

func ping(ip netip.Addr, timeout time.Duration, iface string) (float64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout+2*time.Second)
	defer cancel()
	args := pingArgs(ip, timeout, iface)
	binary := "ping"
	if runtime.GOOS == "darwin" && ip.Is6() {
		binary = "ping6"
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	output, err := cmd.CombinedOutput()
	match := pingDelay.FindSubmatch(output)
	if len(match) != 2 {
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return 0, false
		}
		return 0, false
	}
	var delay float64
	if _, err = fmt.Sscanf(string(match[1]), "%f", &delay); err != nil || math.IsNaN(delay) || math.IsInf(delay, 0) {
		return 0, false
	}
	if delay == 0 {
		delay = 0.01
	}
	return delay, true
}

func pingArgs(ip netip.Addr, timeout time.Duration, iface string) []string {
	if runtime.GOOS == "windows" {
		family := "-4"
		if ip.Is6() {
			family = "-6"
		}
		return []string{family, "-n", "1", "-w", fmt.Sprint(timeout.Milliseconds()), ip.String()}
	}
	if runtime.GOOS == "darwin" && ip.Is6() {
		// macOS provides separate ping6 and ping binaries.
		args := []string{"-n", "-c", "1", "-W", fmt.Sprint(timeout.Milliseconds())}
		if iface != "" {
			args = append(args, "-b", iface)
		}
		return append(args, ip.String())
	}
	args := []string{"-n", "-c", "1"}
	if runtime.GOOS == "darwin" {
		args = append(args, "-W", fmt.Sprint(timeout.Milliseconds()))
		if iface != "" {
			args = append(args, "-b", iface)
		}
	} else {
		seconds := int(math.Ceil(timeout.Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		args = append(args, "-W", fmt.Sprint(seconds))
		if iface != "" {
			args = append(args, "-I", iface)
		}
	}
	args = append(args, ip.String())
	return args
}
