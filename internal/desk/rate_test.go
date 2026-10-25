package desk

import (
	"math/rand/v2"
	"testing"
	"time"
)

func TestRateRisesFivePercentPerSecond(t *testing.T) {
	var r rateState
	t0 := time.Unix(1_700_000_000, 0)
	r.reset(t0, 4_000_000, rateCeil)
	now := t0
	var got []uint32
	for range 6 {
		now = now.Add(time.Second)
		d := r.add(now, 0, 500, 16*time.Millisecond, 16*time.Millisecond)
		got = append(got, d.bitrate)
	}
	want := []uint32{4_000_000, 4_200_000, 4_410_000, 4_630_500, 4_862_025, 5_105_126}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bitrate series %v, want %v", got, want)
		}
	}
}

func TestRateHoldAfterCut(t *testing.T) {
	var r rateState
	t0 := time.Unix(1_700_000_000, 0)
	r.reset(t0.Add(-time.Second), 8_000_000, rateCeil)
	d := r.add(t0, 200, 400, 0, 16*time.Millisecond)
	if !d.changed || d.bitrate != 6_000_000 {
		t.Fatalf("first cut: %+v", d)
	}
	r.winAt = t0.Add(-time.Second)
	d = r.add(t0.Add(200*time.Millisecond), 200, 400, 0, 16*time.Millisecond)
	if d.changed {
		t.Fatalf("cut again inside 500ms: %+v", d)
	}
	r.winAt = t0.Add(200 * time.Millisecond).Add(-time.Second)
	d = r.add(t0.Add(700*time.Millisecond), 200, 400, 0, 16*time.Millisecond)
	if !d.changed || d.bitrate != 4_500_000 {
		t.Fatalf("cut after the hold: %+v", d)
	}
}

func TestHeavyLossStillCuts(t *testing.T) {
	var r rateState
	t0 := time.Unix(1_700_000_000, 0)
	r.reset(t0, 8_000_000, rateCeil)
	// 收到 200、缺口 2000。loss/packets 会大于 1，不能因此当成没有丢包。
	d := r.add(t0.Add(time.Second), 2000, 200, 16*time.Millisecond, 16*time.Millisecond)
	if !d.congested || !d.changed || d.bitrate != 6_000_000 {
		t.Fatalf("heavy loss: %+v", d)
	}
}

func TestQueueDelayUsesTheWindow(t *testing.T) {
	var r rateState
	t0 := time.Unix(1_700_000_000, 0)
	r.reset(t0, 4_000_000, rateCeil)
	for i := range 9 {
		r.add(t0.Add(time.Duration(i)*100*time.Millisecond), 0, 50, 16*time.Millisecond, 16*time.Millisecond)
	}
	d := r.add(t0.Add(time.Second), 0, 50, 80*time.Millisecond, 16*time.Millisecond)
	if d.changed || d.late {
		t.Fatalf("one late report in the second: %+v", d)
	}
	base := t0.Add(time.Second)
	for i := range 9 {
		r.add(base.Add(time.Duration(i)*100*time.Millisecond), 0, 50, 40*time.Millisecond, 16*time.Millisecond)
	}
	d = r.add(base.Add(time.Second), 0, 50, 40*time.Millisecond, 16*time.Millisecond)
	if !d.late || !d.changed || d.bitrate != 3_000_000 {
		t.Fatalf("sustained queue delay: %+v", d)
	}
}

func TestTwoPercentNoiseDoesNotCut(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	var r rateState
	t0 := time.Unix(1_700_000_000, 0)
	r.reset(t0, 4_000_000, rateCeil)
	now := t0
	for range 30 {
		now = now.Add(time.Second)
		loss := 0
		for range 500 {
			if rng.Float64() < 0.02 {
				loss++
			}
		}
		d := r.add(now, uint32(loss), uint32(500-loss), 16*time.Millisecond, 16*time.Millisecond)
		if d.changed && d.bitrate < d.from {
			t.Fatalf("cut on 2%% noise: %+v", d)
		}
	}
	if r.bitrate <= 4_000_000 {
		t.Fatalf("bitrate did not rise, got %d", r.bitrate)
	}
}

func TestRateSettlesBelowAFiveMegabitCap(t *testing.T) {
	var r rateState
	startBit := PictureBitrate(1920, 1080)
	t0 := time.Unix(1_700_000_000, 0)
	r.reset(t0, startBit, rateCeil)
	const link = 5_000_000
	now := t0
	var limited []uint32
	for i := range 30 {
		now = now.Add(time.Second)
		br := r.bitrate
		lossRate := 0.02
		if br > link {
			over := float64(br-link) / float64(br)
			lossRate = over + 0.02*(1-over)
		}
		sent := uint32(600)
		loss := uint32(lossRate * float64(sent))
		if loss > sent {
			loss = sent
		}
		d := r.add(now, loss, sent-loss, 16*time.Millisecond, 16*time.Millisecond)
		if i >= 8 {
			limited = append(limited, d.bitrate)
		}
	}
	for _, b := range limited {
		if b < 3_000_000 || b > 5_200_000 {
			t.Fatalf("bitrate %d left 3–5.2 Mbps during the cap: %v", b, limited)
		}
	}
	if limited[0] <= rateFloor {
		t.Fatalf("stuck at the floor: %v", limited)
	}
	unlimitAt := now
	var back time.Duration
	for range 20 {
		now = now.Add(time.Second)
		d := r.add(now, 12, 600, 16*time.Millisecond, 16*time.Millisecond)
		if back == 0 && d.bitrate >= startBit {
			back = now.Sub(unlimitAt)
		}
	}
	if back == 0 || back > 20*time.Second {
		t.Fatalf("returned to start %d in %s, now %d", startBit, back, r.bitrate)
	}
}

func TestIdleScreenDoesNotRaiseRate(t *testing.T) {
	var r rateState
	t0 := time.Unix(1_700_000_000, 0)
	r.reset(t0, 8_000_000, rateCeil)
	now := t0
	// 画面静止：每秒只有十几个包，没丢包也不加
	for range 10 {
		now = now.Add(time.Second)
		if d := r.add(now, 0, 12, 0, 16*time.Millisecond); d.changed {
			t.Fatalf("raised on an idle window: %+v", d)
		}
	}
	// 画面动起来、链路扛得住，照常加
	now = now.Add(time.Second)
	if d := r.add(now, 0, 800, 16*time.Millisecond, 16*time.Millisecond); !d.changed || d.bitrate != 8_400_000 {
		t.Fatalf("busy window: %+v", d)
	}
}

// 帧率忽高忽低（看终端时一秒十几帧）不是排队：手机报了排队延迟就只看它
func TestQueueDelayOverridesFrameGaps(t *testing.T) {
	var r rateState
	t0 := time.Unix(1_700_000_000, 0)
	r.reset(t0, 8_000_000, rateCeil)
	now := t0
	for range 10 {
		for range 10 {
			now = now.Add(100 * time.Millisecond)
			r.addQueue(5 * time.Millisecond)
			if d := r.add(now, 0, 30, 68*time.Millisecond, 17*time.Millisecond); d.evaluated && d.congested {
				t.Fatalf("variable frame rate taken for congestion: %+v", d)
			}
		}
	}
	// 真排队：每帧多排 80 毫秒
	for range 10 {
		now = now.Add(100 * time.Millisecond)
		r.addQueue(80 * time.Millisecond)
		if d := r.add(now, 0, 600, 17*time.Millisecond, 17*time.Millisecond); d.evaluated {
			if !d.late || !d.changed || d.bitrate >= d.from {
				t.Fatalf("queue not cut: %+v", d)
			}
			return
		}
	}
	t.Fatal("no decision")
}

// 码率低到档位以下就降帧率，往回升要高出档位 30%
func TestFPSFollowsBitrate(t *testing.T) {
	for _, c := range []struct {
		bitrate, cur, want uint32
	}{
		{8_000_000, 60, 60},
		{2_000_000, 60, 30},
		{1_000_000, 30, 15},
		{500_000, 15, 10},
		{400_000, 10, 10},
		// 回升：刚过档位不升，高出 30% 才升；先升到够得着的那一档
		{1_600_000, 15, 15},
		{2_000_000, 15, 30},
		{1_600_000, 10, 15},
		{3_100_000, 30, 30},
		{4_000_000, 30, 60},
	} {
		if got := fpsFor(c.bitrate, c.cur); got != c.want {
			t.Errorf("fpsFor(%d, %d) = %d, want %d", c.bitrate, c.cur, got, c.want)
		}
	}
}
