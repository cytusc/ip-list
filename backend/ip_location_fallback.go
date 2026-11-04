package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"time"
)

// Keep the aggregator's majority policy when it returns valid data, including
// conflicts. Only transport, challenge or payload failures use direct sources.
func lookupReliableIPLocation(ctx context.Context, client *http.Client, origin, ip string) (ipLocation, error) {
	primaryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	location, err := lookupIPLocation(primaryCtx, client, origin, ip)
	cancel()
	if err == nil || ctx.Err() != nil {
		return location, err
	}
	return lookupDirectIPLocation(ctx, client, ip)
}

func lookupDirectIPLocation(parent context.Context, client *http.Client, ip string) (ipLocation, error) {
	if err := parent.Err(); err != nil {
		return ipLocation{}, err
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	type result struct {
		name   string
		record ipGeoRecord
	}
	providers := []struct{ name, target string }{
		{"IPInfo", "https://ipinfo.io/" + ip + "/json"},
		{"DB-IP", "https://api.db-ip.com/v2/free/" + ip},
		{"IPWho.is", "https://ipwho.is/" + ip},
	}
	results := make(chan result, len(providers))
	for _, provider := range providers {
		go func() {
			record, err := lookupDirectIPSource(ctx, client, provider.target, ip)
			if err != nil {
				results <- result{}
				return
			}
			results <- result{provider.name, record}
		}()
	}
	geo := map[string]ipGeoRecord{}
	for range providers {
		select {
		case r := <-results:
			if r.name != "" {
				geo[r.name] = r.record
			}
		case <-ctx.Done():
			if parent.Err() != nil {
				return ipLocation{}, parent.Err()
			}
			return consensusIPLocation(geo)
		}
	}
	if err := parent.Err(); err != nil {
		return ipLocation{}, err
	}
	return consensusIPLocation(geo)
}

func lookupDirectIPSource(ctx context.Context, client *http.Client, target, ip string) (ipGeoRecord, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ipGeoRecord{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "IPGuard-Location/1.0")
	response, err := client.Do(req)
	if err != nil {
		return ipGeoRecord{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ipGeoRecord{}, fmt.Errorf("定位服务 HTTP %d", response.StatusCode)
	}
	var data struct {
		IP          string `json:"ip"`
		IPAddress   string `json:"ipAddress"`
		Country     string `json:"country"`
		CountryCode string `json:"country_code"`
		DBCountry   string `json:"countryCode"`
		Region      string `json:"region"`
		State       string `json:"stateProv"`
		City        string `json:"city"`
		Success     *bool  `json:"success"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&data); err != nil {
		return ipGeoRecord{}, err
	}
	if data.IP == "" {
		data.IP = data.IPAddress
	}
	got, err := netip.ParseAddr(data.IP)
	want, wantErr := netip.ParseAddr(ip)
	if err != nil || wantErr != nil || got != want || (data.Success != nil && !*data.Success) {
		return ipGeoRecord{}, errors.New("定位服务返回的 IP 无效")
	}
	country := data.CountryCode
	if country == "" {
		country = data.DBCountry
	}
	if country == "" {
		country = data.Country
	}
	if dnsCountry(country) == "" {
		return ipGeoRecord{}, errors.New("定位服务未返回有效国家代码")
	}
	if data.Region == "" {
		data.Region = data.State
	}
	return ipGeoRecord{Country: country, Region: data.Region, City: data.City}, nil
}
