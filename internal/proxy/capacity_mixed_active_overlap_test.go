package proxy

import (
	"testing"
	"time"
)

func TestCapacityOverlapRequiresRealTransferIntervals(t *testing.T) {
	base := time.Unix(1000, 0)
	at := func(from, to int) capacityActiveInterval {
		return capacityActiveInterval{start: base.Add(time.Duration(from) * time.Millisecond), end: base.Add(time.Duration(to) * time.Millisecond)}
	}
	// The long worker remains alive from 0ms to 100ms but actually transfers
	// only at 0..20ms and 70..90ms. A short request in the 20..70ms idle gap
	// must NEVER pass the overlap gate simply because the worker exists.
	long := []capacityActiveInterval{at(0, 20), at(70, 90)}
	if n := capacityCountTrueTransferOverlaps(long, []capacityActiveInterval{at(30, 50)}); n != 0 {
		t.Fatalf("idle long worker falsely reported %d real overlaps", n)
	}
	if n := capacityCountTrueTransferOverlaps(long, []capacityActiveInterval{at(20, 70)}); n != 0 {
		t.Fatalf("boundary-only intersection reported %d real overlaps", n)
	}
	if n := capacityCountTrueTransferOverlaps(long, []capacityActiveInterval{at(10, 30), at(85, 95), at(30, 50)}); n != 2 {
		t.Fatalf("real write/read overlap count=%d want=2", n)
	}
}
