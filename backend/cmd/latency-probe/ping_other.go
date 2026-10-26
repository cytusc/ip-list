//go:build !windows

package main

import (
	"net/netip"
	"time"
)

func windowsPing(_ netip.Addr, _ time.Duration, _ string) (float64, bool) { return 0, false }
