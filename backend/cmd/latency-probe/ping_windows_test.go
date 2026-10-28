package main

import (
	"net/netip"
	"testing"
	"time"
)

// Run on Windows to exercise real API calls and native structure layouts.
func TestWindowsNativeICMPLoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "::1"} {
		t.Run(address, func(t *testing.T) {
			ip := netip.MustParseAddr(address)
			delay, success, err := windowsICMPEcho(ip, ip, time.Second, "loopback")
			if err != nil || !success || delay <= 0 {
				t.Fatalf("native echo failed: delay=%v success=%v error=%v", delay, success, err)
			}
		})
	}
}
