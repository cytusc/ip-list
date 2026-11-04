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

	"github.com/jackc/pgx/v5"
)

const ipLocationPolicyVersion = 3
const ipLocationCachePrefix = "ipfilter:ip-location:v3:"

var errLocationConflict = errors.New("归属地数据库无多数一致结果，等待人工确认")

type ipLocationSource struct {
	Name    string `json:"name"`
	Country string `json:"country"`
	Region  string `json:"region"`
	City    string `json:"city"`
}
type ipLocation struct {
	Country string             `json:"country"`
	Region  string             `json:"region"`
	City    string             `json:"city"`
	Status  string             `json:"status,omitempty"`
	Source  string             `json:"source,omitempty"`
	Sources []ipLocationSource `json:"sources,omitempty"`
}
type ipGeoRecord struct {
	Country string `json:"country_code"`
	Region  string `json:"subdivisions"`
	City    string `json:"city"`
}

// Count each supported database once. Require two agreeing sources and a strict
// majority of valid results. ASN, registration country and latency are not votes.
func consensusIPLocation(geo map[string]ipGeoRecord) (ipLocation, error) {
	result := ipLocation{Status: "conflict", Sources: []ipLocationSource{}}
	votes := map[string]int{}
	for _, aliases := range [][]string{{"MaxMind"}, {"IPInfo", "IPInfo.io", "IPinfo", "ipinfo"}, {"DB-IP"}, {"IP2Location"}, {"IPWho.is"}} {
		for _, name := range aliases {
			record, ok := geo[name]
			country := strings.ToUpper(strings.TrimSpace(record.Country))
			if !ok || dnsCountry(country) == "" {
				continue
			}
			result.Sources = append(result.Sources, ipLocationSource{Name: aliases[0], Country: country, Region: strings.TrimSpace(record.Region), City: strings.TrimSpace(record.City)})
			votes[country]++
			break
		}
	}
	if len(result.Sources) == 0 {
		return ipLocation{}, errors.New("定位服务未返回有效的归属地信息")
	}
	if len(result.Sources) == 1 {
		result.Status = "insufficient"
	}
	for _, source := range result.Sources {
		if votes[source.Country] >= 2 && votes[source.Country]*2 > len(result.Sources) {
			chosen := source
			score := func(s ipLocationSource) int {
				n := 0
				if s.Region != "" {
					n++
				}
				if s.City != "" {
					n++
				}
				return n
			}
			for _, candidate := range result.Sources {
				if candidate.Country == source.Country && score(candidate) > score(chosen) {
					chosen = candidate
				}
			}
			result.Country, result.Region, result.City = chosen.Country, chosen.Region, chosen.City
			result.Status, result.Source = "consensus", chosen.Name
			return result, nil
		}
	}
	return result, nil
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
				IP  string                 `json:"ip"`
				Geo map[string]ipGeoRecord `json:"geo"`
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
		return consensusIPLocation(payload.Data.Geo)
	}
	return ipLocation{}, errors.New("IPPure 签名验证失败")
}

// Manual corrections are separate from the automatic cache, so concurrent
// lookups and cache refreshes cannot overwrite them.
func (a *app) resolveIPLocation(ctx context.Context, ip string) (ipLocation, error) {
	if a.db != nil {
		var manual ipLocation
		err := a.db.QueryRow(ctx, "SELECT country,region,city FROM ip_location_overrides WHERE ip=$1::inet", ip).Scan(&manual.Country, &manual.Region, &manual.City)
		if err == nil {
			manual.Status = "manual"
			manual.Source = "人工纠正"
			return manual, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return ipLocation{}, err
		}
		var raw []byte
		var country string
		err = a.db.QueryRow(ctx, "SELECT country,location FROM dns_ip_locations WHERE ip=$1::inet AND policy_version=$2 AND checked_at>now()-CASE WHEN country='' THEN interval '5 minutes' ELSE interval '24 hours' END", ip, ipLocationPolicyVersion).Scan(&country, &raw)
		var cached ipLocation
		if err == nil && json.Unmarshal(raw, &cached) == nil {
			cached.Country = country
			return cached, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return ipLocation{}, err
		}
	}
	if a.redis != nil {
		raw, err := a.redis.Get(ctx, ipLocationCachePrefix+ip).Bytes()
		var cached ipLocation
		if err == nil && json.Unmarshal(raw, &cached) == nil {
			a.cacheListCountry(ctx, ip, cached)
			return cached, nil
		}
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	location, err := lookupReliableIPLocation(lookupCtx, http.DefaultClient, "https://api.123169.xyz", ip)
	if err != nil {
		return ipLocation{}, err
	}
	a.cacheListCountry(ctx, ip, location)
	if a.redis != nil {
		raw, _ := json.Marshal(location)
		ttl := 24 * time.Hour
		if location.Country == "" {
			ttl = 5 * time.Minute
		}
		_ = a.redis.Set(ctx, ipLocationCachePrefix+ip, raw, ttl).Err()
	}
	return location, nil
}

func locationRequestIP(w http.ResponseWriter, r *http.Request) (string, bool) {
	ip, err := netip.ParseAddr(r.URL.Query().Get("ip"))
	if err != nil || !publicMetadataPrefix(netip.PrefixFrom(ip, ip.BitLen())) {
		apiError(w, 400, "请输入公网 IP 地址", nil)
		return "", false
	}
	return ip.String(), true
}

func (a *app) availableIPLocation(w http.ResponseWriter, r *http.Request) {
	ip, ok := locationRequestIP(w, r)
	if !ok {
		return
	}
	location, err := a.resolveIPLocation(r.Context(), ip)
	if err != nil {
		location = ipLocation{Status: "unavailable"}
	}
	respond(w, 200, location)
}

func (a *app) saveIPLocationOverride(w http.ResponseWriter, r *http.Request) {
	ip, ok := locationRequestIP(w, r)
	if !ok {
		return
	}
	var input ipLocation
	if !decode(w, r, &input) {
		return
	}
	input.Country = strings.ToUpper(strings.TrimSpace(input.Country))
	if dnsCountry(input.Country) == "" || len(input.Region) > 200 || len(input.City) > 200 {
		apiError(w, 422, "请选择有效的国家或地区，省市信息最多 200 字节", nil)
		return
	}
	_, err := a.db.Exec(r.Context(), `INSERT INTO ip_location_overrides(ip,country,region,city) VALUES($1::inet,$2,$3,$4) ON CONFLICT(ip) DO UPDATE SET country=excluded.country,region=excluded.region,city=excluded.city,updated_at=now()`, ip, input.Country, strings.TrimSpace(input.Region), strings.TrimSpace(input.City))
	if err != nil {
		dbError(w, err)
		return
	}
	input.Status, input.Source, input.Sources = "manual", "人工纠正", nil
	respond(w, 200, input)
}

func (a *app) deleteIPLocationOverride(w http.ResponseWriter, r *http.Request) {
	ip, ok := locationRequestIP(w, r)
	if !ok {
		return
	}
	if _, err := a.db.Exec(r.Context(), "DELETE FROM ip_location_overrides WHERE ip=$1::inet", ip); err != nil {
		dbError(w, err)
		return
	}
	a.availableIPLocation(w, r)
}

func (a *app) cacheListCountry(ctx context.Context, ip string, location ipLocation) {
	if a.db == nil {
		return
	}
	if dnsCountry(location.Country) == "" && location.Status != "conflict" && location.Status != "insufficient" {
		return
	}
	cacheCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	raw, _ := json.Marshal(location)
	_, _ = a.db.Exec(cacheCtx, `INSERT INTO dns_ip_locations(ip,country,location,policy_version) VALUES($1::inet,$2,$3,$4) ON CONFLICT(ip) DO UPDATE SET country=excluded.country,location=excluded.location,policy_version=excluded.policy_version,checked_at=now()`, ip, location.Country, raw, ipLocationPolicyVersion)
}
