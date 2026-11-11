// Command deskshape forwards UDP and can drop, delay, and rate-limit it.
// It stands in for dnctl/pfctl when those need root.
//
// posse listens on --forward. Point the receiver's hello (tools/desksession) at --listen.
//
//	go run ./tools/deskshape --listen 127.0.0.1:45991 --forward 127.0.0.1:47991 \
//	    --rate 5Mbps --loss 0.02 --delay 30ms --queue 100ms --unlimit-after 25s --seed 1
package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:45991", "address the receiver sends to")
	forward := flag.String("forward", "127.0.0.1:47991", "address posse listens on")
	rate := flag.String("rate", "0", "rate limit in bits/s (0 = unlimited, 5Mbps and 500kbps accepted)")
	loss := flag.Float64("loss", 0, "independent drop probability on each packet")
	delay := flag.Duration("delay", 0, "fixed delay added to every packet")
	queue := flag.Duration("queue", 100*time.Millisecond, "drop a packet that would wait longer than this")
	unlimit := flag.Duration("unlimit-after", 0, "turn the rate limit off after this long")
	seed := flag.Uint64("seed", 1, "RNG seed for drops")
	flag.Parse()

	bits, err := parseRate(*rate)
	if err != nil {
		log.Fatal(err)
	}
	down, err := net.ListenPacket("udp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	defer down.Close()
	posseAddr, err := net.ResolveUDPAddr("udp", *forward)
	if err != nil {
		log.Fatal(err)
	}
	// DialUDP 连上 47991 之后收不到 Posse 的回包，视频方向字节数为 0。
	// 未连接、绑在固定端口上就能收到。
	up, err := net.ListenPacket("udp", "127.0.0.1:45992")
	if err != nil {
		log.Fatal(err)
	}
	defer up.Close()
	enlarge(down)
	enlarge(up)

	toPosse := newLane("to-posse", *loss, *delay, *queue, bits, *seed)
	toPhone := newLane("to-phone", *loss, *delay, *queue, bits, *seed+1)
	go toPosse.loop()
	go toPhone.loop()
	var client atomic.Value
	go readLoop(down, func(pkt []byte, addr net.Addr) {
		client.Store(cloneUDP(addr))
		toPosse.forward(pkt, func(b []byte) { _, _ = up.WriteTo(b, posseAddr) })
	})
	go readLoop(up, func(pkt []byte, _ net.Addr) {
		v := client.Load()
		if v == nil {
			return
		}
		toPhone.forward(pkt, func(b []byte) { _, _ = down.WriteTo(b, v.(net.Addr)) })
	})
	log.Printf("deskshape listen %s -> %s rate=%d loss=%.3f delay=%s queue=%s", *listen, *forward, bits, *loss, *delay, *queue)
	if *unlimit > 0 {
		time.AfterFunc(*unlimit, func() {
			toPosse.setRate(0)
			toPhone.setRate(0)
			log.Printf("deskshape: rate limit off")
		})
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	for {
		select {
		case <-sig:
			log.Printf("deskshape: stop")
			return
		case <-tick.C:
			vs, vb, vl, vq := toPhone.take()
			rs, _, rl, rq := toPosse.take()
			log.Printf("deskshape: video sent=%d bytes=%d loss=%d queue=%d ctrl sent=%d loss=%d queue=%d", vs, vb, vl, vq, rs, rl, rq)
		}
	}
}

type lane struct {
	name  string
	loss  float64
	delay time.Duration
	maxQ  time.Duration
	rng   *rand.Rand
	mu    sync.Mutex
	rate  float64 // bytes per second; 0 is unlimited
	next  time.Time
	sent  uint64
	bytes uint64
	dropL uint64
	dropQ uint64
	q     []queued
	cond  *sync.Cond
}

type queued struct {
	at    time.Time
	data  []byte
	write func([]byte)
}

func newLane(name string, loss float64, delay, maxQ time.Duration, bitsPerSec int, seed uint64) *lane {
	l := &lane{
		name: name, loss: loss, delay: delay, maxQ: maxQ,
		rng: rand.New(rand.NewPCG(seed, 1)),
	}
	l.cond = sync.NewCond(&l.mu)
	l.setRate(bitsPerSec)
	return l
}

func (l *lane) setRate(bitsPerSec int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if bitsPerSec <= 0 {
		l.rate = 0
		return
	}
	l.rate = float64(bitsPerSec) / 8
}

func (l *lane) forward(pkt []byte, write func([]byte)) {
	b := append([]byte(nil), pkt...)
	at, ok := l.schedule(b, time.Now())
	if !ok {
		return
	}
	l.mu.Lock()
	l.q = append(l.q, queued{at: at, data: b, write: write})
	l.cond.Signal()
	l.mu.Unlock()
}

// loop 按放行时间顺序写出。每个包各起一个定时器时，延迟相同的包不会按到达顺序出去，
// 接收端会把乱序当成丢包。
func (l *lane) loop() {
	for {
		l.mu.Lock()
		for len(l.q) == 0 {
			l.cond.Wait()
		}
		item := l.q[0]
		l.q = l.q[1:]
		l.mu.Unlock()
		if d := time.Until(item.at); d > 0 {
			time.Sleep(d)
		}
		item.write(item.data)
	}
}

func (l *lane) schedule(pkt []byte, now time.Time) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.loss > 0 && l.rng.Float64() < l.loss {
		l.dropL++
		return now, false
	}
	release := now
	if l.rate > 0 {
		if l.next.Before(now) {
			l.next = now
		}
		release = l.next
		advance := time.Duration(float64(len(pkt)) / l.rate * float64(time.Second))
		l.next = l.next.Add(advance)
		if release.Sub(now) > l.maxQ {
			l.next = release
			l.dropQ++
			return now, false
		}
	}
	l.sent++
	l.bytes += uint64(len(pkt))
	return release.Add(l.delay), true
}

func (l *lane) take() (sent, bytes, loss, queue uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	sent, bytes, loss, queue = l.sent, l.bytes, l.dropL, l.dropQ
	l.sent, l.bytes, l.dropL, l.dropQ = 0, 0, 0, 0
	return
}

func parseRate(s string) (int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "0" {
		return 0, nil
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "mbps"):
		mult = 1_000_000
		s = strings.TrimSuffix(s, "mbps")
	case strings.HasSuffix(s, "m"):
		mult = 1_000_000
		s = strings.TrimSuffix(s, "m")
	case strings.HasSuffix(s, "kbps"):
		mult = 1_000
		s = strings.TrimSuffix(s, "kbps")
	case strings.HasSuffix(s, "k"):
		mult = 1_000
		s = strings.TrimSuffix(s, "k")
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("deskshape: bad --rate %q", s)
	}
	return int(v * mult), nil
}

func enlarge(pc net.PacketConn) {
	u, ok := pc.(*net.UDPConn)
	if !ok {
		return
	}
	_ = u.SetReadBuffer(4 << 20)
	_ = u.SetWriteBuffer(4 << 20)
}

func cloneUDP(a net.Addr) net.Addr {
	u, ok := a.(*net.UDPAddr)
	if !ok {
		return a
	}
	c := *u
	if u.IP != nil {
		c.IP = append(net.IP(nil), u.IP...)
	}
	return &c
}

func readLoop(pc net.PacketConn, fn func([]byte, net.Addr)) {
	buf := make([]byte, 2048)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		fn(buf[:n], addr)
	}
}
