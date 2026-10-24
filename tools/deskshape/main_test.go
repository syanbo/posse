package main

import (
	"testing"
	"time"
)

func TestParseRate(t *testing.T) {
	bits, err := parseRate("5Mbps")
	if err != nil || bits != 5_000_000 {
		t.Fatalf("5Mbps = %d %v", bits, err)
	}
	bits, err = parseRate("0")
	if err != nil || bits != 0 {
		t.Fatalf("0 = %d %v", bits, err)
	}
}

func TestLaneDropsTheBurstThatDoesNotFit(t *testing.T) {
	l := newLane("t", 0, 0, 50*time.Millisecond, 5_000_000, 1)
	now := time.Unix(1_700_000_000, 0)
	kept, dropped := 0, 0
	for range 100 {
		if _, ok := l.schedule(make([]byte, 1200), now); ok {
			kept++
		} else {
			dropped++
		}
	}
	if kept < 10 || dropped < 50 {
		t.Fatalf("kept %d dropped %d", kept, dropped)
	}
	l.setRate(0)
	if _, ok := l.schedule(make([]byte, 1200), now); !ok {
		t.Fatal("unlimited packet was dropped")
	}
}

func TestLaneLossIsAboutTwoPercent(t *testing.T) {
	l := newLane("t", 0.02, 0, time.Second, 0, 1)
	now := time.Unix(1_700_000_000, 0)
	var dropped int
	for range 2000 {
		if _, ok := l.schedule(make([]byte, 100), now); !ok {
			dropped++
		}
	}
	if dropped < 15 || dropped > 70 {
		t.Fatalf("drops %d out of 2000", dropped)
	}
}
