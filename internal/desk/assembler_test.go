package desk

import "encoding/binary"

// 测试用：把 Packetize 切出来的 RTP 分片拼回一帧，核对发送端的分片能原样拼回。手机上收包拼帧的是原生的 DeskSession。

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
