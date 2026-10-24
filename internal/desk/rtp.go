package desk

import "encoding/binary"

const (
	rtpClock       = 90_000
	rtpPayloadHEVC = 96
	rtpPayloadH264 = 97
	hevcFU         = 49
	h264FUA        = 28 // RFC 6184 FU-A
	// 视频明文：1 字节标志 + 帧号 + 采集时刻 + RTP。标志最低位是关键帧。
	videoPrefix = 17
)

// Packetize 把 Annex B 帧拆成不超过 PacketLimit 的视频明文（前缀 + RTP）。
// codec 决定 RTP 负载类型和分片：HEVC 用 RFC 7798，H.264 用 RFC 6184 的 FU-A。
func Packetize(f Frame, seq0 uint16, codec byte) [][]byte {
	nals := splitAnnexB(f.Payload)
	var out [][]byte
	seq := seq0
	ts := uint32(f.Number * rtpClock / 60)
	pt := byte(rtpPayloadHEVC)
	h264 := codec == CodecH264
	if h264 {
		pt = rtpPayloadH264
	}
	room := PacketLimit - (1 + 8 + 16) - videoPrefix - 12
	for i, nal := range nals {
		last := i == len(nals)-1
		if len(nal) <= room {
			out = append(out, videoPacket(f, seq, ts, pt, last, nal))
			seq++
			continue
		}
		if h264 {
			out, seq = fragmentH264(out, f, nal, seq, ts, pt, last, room)
		} else {
			out, seq = fragmentHEVC(out, f, nal, seq, ts, pt, last, room)
		}
	}
	return out
}

func fragmentHEVC(out [][]byte, f Frame, nal []byte, seq uint16, ts uint32, pt byte, last bool, room int) ([][]byte, uint16) {
	frag := room - 3
	if frag < 1 {
		frag = 1
	}
	if len(nal) < 2 {
		return out, seq
	}
	first := true
	for off := 2; off < len(nal); {
		end := off + frag
		if end > len(nal) {
			end = len(nal)
		}
		tail := end == len(nal)
		hdr := []byte{(nal[0] & 0x81) | (hevcFU << 1), nal[1], fuHeader(first, tail, nal)}
		body := append(hdr, nal[off:end]...)
		out = append(out, videoPacket(f, seq, ts, pt, last && tail, body))
		seq++
		off = end
		first = false
	}
	return out, seq
}

func fragmentH264(out [][]byte, f Frame, nal []byte, seq uint16, ts uint32, pt byte, last bool, room int) ([][]byte, uint16) {
	frag := room - 2
	if frag < 1 {
		frag = 1
	}
	if len(nal) < 1 {
		return out, seq
	}
	first := true
	for off := 1; off < len(nal); {
		end := off + frag
		if end > len(nal) {
			end = len(nal)
		}
		tail := end == len(nal)
		var fu byte = nal[0] & 0x1f
		if first {
			fu |= 0x80
		}
		if tail {
			fu |= 0x40
		}
		body := append([]byte{(nal[0] & 0xe0) | h264FUA, fu}, nal[off:end]...)
		out = append(out, videoPacket(f, seq, ts, pt, last && tail, body))
		seq++
		off = end
		first = false
	}
	return out, seq
}

func fuHeader(start, end bool, nal []byte) byte {
	var b byte
	if start {
		b |= 0x80
	}
	if end {
		b |= 0x40
	}
	if len(nal) > 0 {
		b |= (nal[0] >> 1) & 0x3f
	}
	return b
}

func videoPacket(f Frame, seq uint16, ts uint32, pt byte, marker bool, payload []byte) []byte {
	rtp := make([]byte, 12+len(payload))
	rtp[0] = 0x80
	rtp[1] = pt
	if marker {
		rtp[1] |= 0x80
	}
	binary.BigEndian.PutUint16(rtp[2:4], seq)
	binary.BigEndian.PutUint32(rtp[4:8], ts)
	binary.BigEndian.PutUint32(rtp[8:12], 0x48340001)
	copy(rtp[12:], payload)
	plain := make([]byte, videoPrefix+len(rtp))
	if f.Keyframe {
		plain[0] = 1
	}
	binary.BigEndian.PutUint64(plain[1:9], f.Number)
	binary.BigEndian.PutUint64(plain[9:17], uint64(f.CaptureUnixNano))
	copy(plain[17:], rtp)
	return plain
}

func randomAccess(b []byte) bool {
	for _, nal := range splitAnnexB(b) {
		if len(nal) == 0 {
			continue
		}
		switch (nal[0] >> 1) & 0x3f {
		case 16, 17, 18, 19, 20, 21:
			return true
		}
	}
	return false
}

func splitAnnexB(b []byte) [][]byte {
	// at 是起始码后面的第一个字节，code 是起始码长度（3 或 4）。
	// 上一条 NAL 要停在下一条起始码之前，不能把 00 00 01 算进负载。
	type mark struct{ at, code int }
	var marks []mark
	for i := 0; i+3 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			marks = append(marks, mark{i + 3, 3})
			i += 2
			continue
		}
		if i+4 <= len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 0 && b[i+3] == 1 {
			marks = append(marks, mark{i + 4, 4})
			i += 3
		}
	}
	out := make([][]byte, 0, len(marks))
	for i, m := range marks {
		e := len(b)
		if i+1 < len(marks) {
			e = marks[i+1].at - marks[i+1].code
		}
		if m.at < e {
			out = append(out, b[m.at:e])
		}
	}
	return out
}

// Assembler 把一帧的 RTP 分片拼回 Annex B。序号出现缺口时丢掉这一帧，不交出残帧。
type Assembler struct {
	frame   uint64
	capture int64
	key     bool
	expect  uint16
	have    bool
	broken  bool
	nals    [][]byte
	fu      []byte
}

func (a *Assembler) Push(plain []byte) (*Frame, error) {
	if len(plain) < videoPrefix+12 {
		return nil, errPacket
	}
	key := plain[0]&1 != 0
	number := binary.BigEndian.Uint64(plain[1:9])
	capture := int64(binary.BigEndian.Uint64(plain[9:17]))
	rtp := plain[17:]
	seq := binary.BigEndian.Uint16(rtp[2:4])
	marker := rtp[1]&0x80 != 0
	pt := rtp[1] & 0x7f
	payload := rtp[12:]
	if !a.have || number != a.frame {
		a.reset(number, capture, key, seq)
	} else if seq != a.expect {
		a.broken = true
		a.fu = nil
		a.nals = nil
		a.expect = seq
		return nil, errPacket
	}
	a.expect++
	if pt == rtpPayloadH264 {
		a.takeH264(payload)
	} else {
		a.takeHEVC(payload)
	}
	if !marker {
		return nil, nil
	}
	a.have = false
	if a.broken || len(a.fu) > 0 {
		return nil, errPacket
	}
	var body []byte
	for _, nal := range a.nals {
		body = append(body, 0, 0, 0, 1)
		body = append(body, nal...)
	}
	return &Frame{Kind: KindVideo, Keyframe: a.key, Number: a.frame, CaptureUnixNano: a.capture, Payload: body}, nil
}

func (a *Assembler) takeHEVC(payload []byte) {
	if len(payload) >= 3 && ((payload[0]>>1)&0x3f) == hevcFU {
		piece := payload[3:]
		if payload[2]&0x80 != 0 {
			a.fu = append([]byte(nil), piece...)
		} else {
			a.fu = append(a.fu, piece...)
		}
		if payload[2]&0x40 != 0 {
			nal := append([]byte{payload[0], payload[1]}, a.fu...)
			nal[0] = (payload[0] & 0x81) | ((payload[2] & 0x3f) << 1)
			a.nals = append(a.nals, nal)
			a.fu = nil
		}
		return
	}
	if len(payload) > 0 {
		a.nals = append(a.nals, append([]byte(nil), payload...))
	}
}

func (a *Assembler) takeH264(payload []byte) {
	if len(payload) >= 2 && payload[0]&0x1f == h264FUA {
		piece := payload[2:]
		if payload[1]&0x80 != 0 {
			a.fu = []byte{(payload[0] & 0xe0) | (payload[1] & 0x1f)}
			a.fu = append(a.fu, piece...)
		} else if len(a.fu) > 0 {
			a.fu = append(a.fu, piece...)
		}
		if payload[1]&0x40 != 0 && len(a.fu) > 0 {
			a.nals = append(a.nals, a.fu)
			a.fu = nil
		}
		return
	}
	if len(payload) > 0 {
		a.nals = append(a.nals, append([]byte(nil), payload...))
	}
}

func (a *Assembler) reset(number uint64, capture int64, key bool, seq uint16) {
	a.frame = number
	a.capture = capture
	a.key = key
	a.expect = seq
	a.have = true
	a.broken = false
	a.nals = nil
	a.fu = nil
}
