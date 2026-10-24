package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type networkMetadata struct {
	ASN      string `json:"asn"`
	Provider string `json:"provider"`
}

func publicMetadataPrefix(p netip.Prefix) bool {
	ip := p.Addr()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Is4In6() {
		return false
	}
	for _, reserved := range []string{"100.64.0.0/10", "192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "198.18.0.0/15", "2001:db8::/32"} {
		if netip.MustParsePrefix(reserved).Contains(ip) {
			return false
		}
	}
	return true
}

// RIPEstat supplies the originating ASN and its registered holder, not a
// guessed retail brand. Refuse ambiguous or partially covered prefixes.
func lookupNetworkMetadata(ctx context.Context, client *http.Client, base string, prefix netip.Prefix) (networkMetadata, error) {
	var result networkMetadata
	get := func(endpoint, resource string, target any) error {
		req, err := http.NewRequestWithContext(ctx, "GET", base+"/data/"+endpoint+"/data.json?resource="+url.QueryEscape(resource), nil)
		if err != nil {
			return err
		}
		response, err := client.Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return fmt.Errorf("RIPE HTTP %d", response.StatusCode)
		}
		return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target)
	}
	var network struct {
		Status string
		Data   struct {
			ASNs   []string `json:"asns"`
			Prefix string
		}
	}
	if err := get("network-info", prefix.Addr().String(), &network); err != nil {
		return result, err
	}
	announced, err := netip.ParsePrefix(network.Data.Prefix)
	if network.Status != "ok" || err != nil || len(network.Data.ASNs) != 1 || announced.Bits() > prefix.Bits() || !announced.Contains(prefix.Addr()) {
		return result, errors.New("no unique ASN covering this range")
	}
	asn, err := strconv.ParseUint(network.Data.ASNs[0], 10, 32)
	if err != nil || asn == 0 {
		return result, errors.New("invalid ASN")
	}
	var overview struct {
		Status string
		Data   struct{ Holder string }
	}
	if err := get("as-overview", "AS"+strconv.FormatUint(asn, 10), &overview); err != nil {
		return result, err
	}
	holder := strings.TrimSpace(overview.Data.Holder)
	if overview.Status != "ok" || holder == "" {
		return result, errors.New("missing ASN holder")
	}
	// Keep the existing schema's 100-character provider limit.
	result = networkMetadata{ASN: "AS" + strconv.FormatUint(asn, 10), Provider: string([]rune(holder)[:min(100, len([]rune(holder)))])}
	return result, nil
}

func (a *app) networkMetadata(ctx context.Context, prefix netip.Prefix) (networkMetadata, error) {
	key := "ipfilter:network-metadata:v1:" + prefix.String()
	if a.redis != nil {
		cacheCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		raw, err := a.redis.Get(cacheCtx, key).Bytes()
		cancel()
		var cached networkMetadata
		if err == nil && json.Unmarshal(raw, &cached) == nil && cached.ASN != "" && cached.Provider != "" {
			return cached, nil
		}
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := lookupNetworkMetadata(lookupCtx, http.DefaultClient, "https://stat.ripe.net", prefix)
	if err == nil && a.redis != nil {
		raw, _ := json.Marshal(result)
		cacheCtx, stop := context.WithTimeout(ctx, 150*time.Millisecond)
		_ = a.redis.Set(cacheCtx, key, raw, 24*time.Hour).Err()
		stop()
	}
	return result, err
}
