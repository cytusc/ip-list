package main

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"testing"
	"time"
)

func TestWindowsICMPReply(t *testing.T) {
	for _, address := range []string{"203.0.113.8", "2001:db8::8"} {
		t.Run(address, func(t *testing.T) {
			target := netip.MustParseAddr(address)
			reply := make([]byte, 64)
			offset := 4
			if target.Is6() {
				a := target.As16()
				copy(reply[8:24], a[:])
				offset = 28
			} else {
				a := target.As4()
				copy(reply, a[:])
			}
			binary.LittleEndian.PutUint32(reply[offset+4:], 183)
			if delay, ok, err := parseWindowsICMPReply(reply, target); err != nil || !ok || delay != 183 {
				t.Fatalf("valid reply: %v %v %v", delay, ok, err)
			}
			binary.LittleEndian.PutUint32(reply[offset+4:], 0)
			if delay, ok, err := parseWindowsICMPReply(reply, target); err != nil || !ok || delay != 0.01 {
				t.Fatalf("sub-millisecond reply: %v %v %v", delay, ok, err)
			}
			for _, status := range []uint32{11003, 11010, 11013} {
				binary.LittleEndian.PutUint32(reply[offset:], status)
				if _, ok, err := parseWindowsICMPReply(reply, target); err != nil || ok {
					t.Fatalf("remote failure %d: %v %v", status, ok, err)
				}
			}
			for _, status := range []uint32{11001, 11006, 11008, 11012, 11045, 11050, 5, 87} {
				binary.LittleEndian.PutUint32(reply[offset:], status)
				if _, ok, err := parseWindowsICMPReply(reply, target); err == nil || ok {
					t.Fatalf("local error %d not distinguished: %v %v", status, ok, err)
				}
			}
			binary.LittleEndian.PutUint32(reply[offset:], 0)
			reply[0], reply[8] = 0, 0
			if _, _, err := parseWindowsICMPReply(reply, target); err == nil {
				t.Fatal("wrong peer accepted")
			}
			if _, _, err := parseWindowsICMPReply(reply[:offset+7], target); err == nil {
				t.Fatal("truncated reply accepted")
			}
		})
	}
}

func TestLocalMeasurementErrorsAreNotReportedAsIPFailures(t *testing.T) {
	cfg := settings{IPs: []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"}, Concurrency: 3}
	items := runMeasurements(cfg, func(ip netip.Addr, _ settings, _ time.Duration) (float64, bool, error) {
		switch ip.As4()[3] {
		case 1:
			return 0, false, fmt.Errorf("local API failure")
		case 2:
			return 0, false, nil // Actual target timeout is still reported.
		default:
			return 220, true, nil
		}
	})
	if len(items) != 2 {
		t.Fatalf("expected success and real timeout only: %+v", items)
	}
	for _, item := range items {
		if item.IP == "203.0.113.1" || item.Success != (item.IP == "203.0.113.3") {
			t.Fatalf("local failure misreported: %+v", item)
		}
	}
	items = runMeasurements(cfg, func(netip.Addr, settings, time.Duration) (float64, bool, error) {
		return 0, false, fmt.Errorf("local error")
	})
	if len(items) != 0 {
		t.Fatal("all-local-failure round should not report any invalid IPs")
	}
}
