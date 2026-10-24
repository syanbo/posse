package desk

import (
	"math"
	"sort"
	"time"
)

const (
	rateFloor  = 1_500_000
	rateCeil   = 20_000_000
	rateWindow = time.Second
	rateHold   = 500 * time.Millisecond
	rateStable = 2 * time.Second
	// 排队超过它算堵。Wi-Fi 平时抖十几毫秒，远程桌面多排 40 毫秒手上就能感觉到
	rateQueueLate = 40 * time.Millisecond
)

// rateState 按大约 1 秒的接收报告调码率。
// 单个 100ms 报告里丢一个包就会超过 2%，不能按它降；降完至少隔 rateHold 再降。
// 连续 rateStable 没有拥塞之后，每个窗口加 5%，不超过上限。
// 加码率还要这一窗口真的跑了码率的四分之一以上：画面静止时几乎没有包，没丢包不说明链路扛得住，
// 一直往上加，画面一动就按虚高的码率发。
type rateState struct {
	bitrate uint32
	maxBit  uint32
	stable  time.Time
	lastCut time.Time
	lastUp  time.Time
	winAt   time.Time
	loss    uint32
	packets uint32
	gaps    []time.Duration
	// 手机报的排队延迟。有它就按它判断排队，不再看帧间隔
	queues []time.Duration
}

type rateDecision struct {
	bitrate   uint32
	from      uint32
	changed   bool
	evaluated bool
	congested bool
	loss      float64
	packets   uint32
	late      bool
	gap       time.Duration
	queue     time.Duration
}

// addQueue 收进一条报告里的排队延迟：每帧到达减采集，比最近 10 秒最顺的时候多出来的部分。
func (r *rateState) addQueue(q time.Duration) {
	r.queues = append(r.queues, q)
}

func (r *rateState) reset(now time.Time, bitrate, maxBit uint32) {
	if maxBit > rateCeil || maxBit == 0 {
		maxBit = rateCeil
	}
	if bitrate < rateFloor {
		bitrate = rateFloor
	}
	if bitrate > maxBit {
		bitrate = maxBit
	}
	*r = rateState{bitrate: bitrate, maxBit: maxBit, stable: now, winAt: now}
}

// add 收进一条 100ms 报告。窗口凑满大约 1 秒才做一次决定。
func (r *rateState) add(now time.Time, loss, packets uint32, gap, sendGap time.Duration) rateDecision {
	if r.winAt.IsZero() {
		r.reset(now, r.bitrate, r.maxBit)
	}
	r.loss += loss
	r.packets += packets
	// 画面停住时帧间隔会到几百毫秒，那不是排队变长。
	if gap >= 8*time.Millisecond && gap < 100*time.Millisecond {
		r.gaps = append(r.gaps, gap)
	}
	d := rateDecision{bitrate: r.bitrate, from: r.bitrate}
	if now.Sub(r.winAt) < rateWindow {
		return d
	}
	d.evaluated = true
	d.packets = r.packets
	if total := uint64(r.loss) + uint64(r.packets); total > 0 {
		d.loss = float64(r.loss) / float64(total)
	}
	d.gap = medianGap(r.gaps)
	if len(r.queues) > 0 {
		d.queue = medianGap(r.queues)
		d.late = d.queue > rateQueueLate
	} else {
		d.late = queueLate(r.gaps, sendGap)
	}
	over := lossClearlyOver(r.loss, r.packets)
	d.congested = over || d.late
	next := r.bitrate
	if d.congested {
		if r.lastCut.IsZero() || now.Sub(r.lastCut) >= rateHold {
			next = r.bitrate * 3 / 4
			r.lastCut = now
			r.stable = now
			r.lastUp = time.Time{}
		}
	} else if now.Sub(r.stable) >= rateStable && r.bitrate < r.maxBit && (r.lastUp.IsZero() || now.Sub(r.lastUp) >= time.Second) &&
		carried(r.packets, now.Sub(r.winAt))*4 >= uint64(r.bitrate) {
		next = r.bitrate + r.bitrate/20
		if next > r.maxBit {
			next = r.maxBit
		}
		r.lastUp = now
	}
	if next < rateFloor {
		next = rateFloor
	}
	if next > rateCeil {
		next = rateCeil
	}
	d.changed = next != r.bitrate
	d.from = r.bitrate
	r.bitrate = next
	d.bitrate = next
	r.loss = 0
	r.packets = 0
	r.gaps = r.gaps[:0]
	r.queues = r.queues[:0]
	r.winAt = now
	return d
}

// lossClearlyOver 判断这一窗口的丢包是不是明显高于 2%。
// packets 是收到的包，loss 是序号缺口，丢包率 = loss / (loss+packets)。
// 用 loss/packets 时，缺口比收到的还多，比例会大于 1，标准差变成 NaN，反而判定成没拥塞。
// 2% 的随机丢包在 1 秒里会抖到 2% 以上。观测比例减去大约两个标准差后仍超过 2%，
// 才当成链路堵了；刚好贴着 2% 不降，否则会一路砍到下限。
func lossClearlyOver(loss, packets uint32) bool {
	total := uint64(loss) + uint64(packets)
	if total < 50 || loss == 0 {
		return false
	}
	p := float64(loss) / float64(total)
	if p <= 0.02 {
		return false
	}
	if p >= 1 {
		return true
	}
	sd := math.Sqrt(p * (1 - p) / float64(total))
	return p-2*sd > 0.02
}

func queueLate(gaps []time.Duration, sendGap time.Duration) bool {
	if len(gaps) < 5 {
		return false
	}
	if sendGap <= 0 {
		sendGap = 17 * time.Millisecond
	}
	return medianGap(gaps) > sendGap+15*time.Millisecond
}

func medianGap(gaps []time.Duration) time.Duration {
	if len(gaps) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), gaps...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

// carried 是这一窗口收到的包折成的每秒比特数，按每个包都满 PacketLimit 算，偏高。
func carried(packets uint32, window time.Duration) uint64 {
	if window <= 0 {
		return 0
	}
	return uint64(packets) * PacketLimit * 8 * uint64(time.Second) / uint64(window)
}
