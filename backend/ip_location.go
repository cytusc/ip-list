package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type ipLocation struct {
	Country string `json:"country"`
	Region  string `json:"region"`
	City    string `json:"city"`
}

// Follow the same key challenge and HMAC signature as node_list's IPPure client.
// Only basic location data is needed; risk and ASN statistics are not queried.
func lookupIPLocation(ctx context.Context, client *http.Client, origin, ip string) (ipLocation, error) {
	target := strings.TrimRight(origin, "/") + "/api/info/ip-basic/" + ip
	var key string
	var offset int64
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return ipLocation{}, err
		}
		req.Header.Set("Origin", "https://ippure.com")
		req.Header.Set("Referer", "https://ippure.com/")
		req.Header.Set("User-Agent", "Mozilla/5.0")
		if key != "" {
			now := time.Now().UnixMilli() + offset
			mac := hmac.New(sha256.New, []byte(key))
			fmt.Fprintf(mac, "GET-%s--%d", target, now)
			req.Header.Set("x-k", key)
			req.Header.Set("x-t", fmt.Sprintf("%d-%s", now, hex.EncodeToString(mac.Sum(nil))))
		}
		response, err := client.Do(req)
		if err != nil {
			return ipLocation{}, err
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if readErr != nil {
			return ipLocation{}, readErr
		}
		if issued := response.Header.Get("x-k"); issued != "" {
			stamp, err := strconv.ParseInt(response.Header.Get("x-t"), 10, 64)
			if err != nil {
				return ipLocation{}, err
			}
			key, offset = issued, stamp-time.Now().UnixMilli()
			continue
		}
		if response.StatusCode != http.StatusOK {
			return ipLocation{}, fmt.Errorf("IPPure HTTP %d", response.StatusCode)
		}
		var payload struct {
			OK   bool `json:"ok"`
			Data struct {
				IP  string `json:"ip"`
				Geo map[string]struct {
					Country string `json:"country_code"`
					Region  string `json:"subdivisions"`
					City    string `json:"city"`
				} `json:"geo"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return ipLocation{}, err
		}
		returned, err := netip.ParseAddr(payload.Data.IP)
		requested, _ := netip.ParseAddr(ip)
		if !payload.OK || err != nil || returned != requested {
			return ipLocation{}, errors.New("IPPure 返回的归属地信息无效")
		}
		// IPPure returns locations per database, rather than top-level fields.
		// Prefer IP2Location's detailed record; fall back without mixing sources.
		for _, source := range []string{"IP2Location", "DB-IP", "MaxMind"} {
			geo := payload.Data.Geo[source]
			country := strings.ToUpper(strings.TrimSpace(geo.Country))
			if len(country) == 2 && country[0] >= 'A' && country[0] <= 'Z' && country[1] >= 'A' && country[1] <= 'Z' {
				return ipLocation{Country: country, Region: strings.TrimSpace(geo.Region), City: strings.TrimSpace(geo.City)}, nil
			}
		}
		return ipLocation{}, errors.New("IPPure 未返回有效的归属地信息")
	}
	return ipLocation{}, errors.New("IPPure 签名验证失败")
}

func (a *app) availableIPLocation(w http.ResponseWriter, r *http.Request) {
	ip, err := netip.ParseAddr(r.URL.Query().Get("ip"))
	if err != nil || !publicMetadataPrefix(netip.PrefixFrom(ip, ip.BitLen())) {
		respond(w, http.StatusBadRequest, map[string]string{"error": "请输入公网 IP 地址"})
		return
	}
	key := "ipfilter:ip-location:v2:" + ip.String()
	if a.redis != nil {
		cacheCtx, cancel := context.WithTimeout(r.Context(), 150*time.Millisecond)
		raw, err := a.redis.Get(cacheCtx, key).Bytes()
		cancel()
		var cached ipLocation
		if err == nil && json.Unmarshal(raw, &cached) == nil {
			respond(w, http.StatusOK, cached)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	location, lookupErr := lookupIPLocation(ctx, http.DefaultClient, "https://api.123169.xyz", ip.String())
	if a.redis != nil && r.Context().Err() == nil {
		ttl := 24 * time.Hour
		if lookupErr != nil {
			ttl = time.Minute
		}
		raw, _ := json.Marshal(location)
		cacheCtx, stop := context.WithTimeout(r.Context(), 150*time.Millisecond)
		_ = a.redis.Set(cacheCtx, key, raw, ttl).Err()
		stop()
	}
	// Unknown location must never prevent the available IP list from being used.
	respond(w, http.StatusOK, location)
}
