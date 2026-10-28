//go:build !windows

package main

import (
	"fmt"
	"net/netip"
	"time"
)

func windowsPing(_ netip.Addr, _ time.Duration, _ string) (float64, bool, error) {
	return 0, false, fmt.Errorf("当前平台不支持 Windows ICMP API")
}
