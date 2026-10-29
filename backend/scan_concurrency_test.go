package main

import (
	"context"
	"testing"
	"time"
)

func TestXrayScanSlotsWaitCancelAndReuse(t *testing.T) {
	// Fill the shared budget as if several protocol jobs were already running.
	for i := 0; i < cap(xrayScanSlots); i++ {
		if !acquireXrayScanSlot(context.Background()) {
			t.Fatal("available slot rejected")
		}
	}
	defer func() {
		for len(xrayScanSlots) > 0 {
			<-xrayScanSlots
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan bool, 1)
	go func() { waiting <- acquireXrayScanSlot(ctx) }()
	select {
	case <-waiting:
		t.Fatal("exceeded shared Xray budget")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case acquired := <-waiting:
		if acquired {
			t.Fatal("cancelled waiter acquired a slot")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled scan remained blocked")
	}

	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { waiting <- acquireXrayScanSlot(ctx) }()
	<-xrayScanSlots // A completed IP frees capacity for another protocol job.
	select {
	case acquired := <-waiting:
		if !acquired {
			t.Fatal("released slot was not reused")
		}
	case <-ctx.Done():
		t.Fatal("next scan did not resume")
	}
}
