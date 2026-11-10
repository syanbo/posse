package mobile

import (
	"encoding/binary"
	"sort"
)

// 大块数据走二进制帧（posse.bulk.v1）。JSON 回复里的文件内容是 base64，比原始数据大三分之一；
// 整条消息还一直占着发送锁，按键回显和 ping 的回复只能排在后面。
//
// 电脑 → 手机：回复里的 []byte 字段拆成 bulkChunk 大小的块先发，每块单独拿一次发送锁，
// 别的消息能插在块之间；最后发 JSON 回复，字段换成 "blobs": {"字段路径": 块编号}（嵌套的用点连起来）。
// 同一条连接按顺序到达，手机收到回复时这些块都已经到了。
// 手机 → 电脑：上传的分段（clipboard.startImageUpload 带 bytes 时）也走二进制帧，不用每段等一次回复。
// 每收下一段回一个确认帧（内容是 u64 已收字节数，小端），手机按它限制路上的数据量：一下子全塞进发送队列，
// 同一连接的按键、ping 要排在整张图后面，手机上行占满还会拖慢看桌面的输入和接收报告。
// startImageUpload 的回复带 "ack": true 表示会回确认，旧电脑没有，手机照旧一口气发。
//
// 帧：u8 0x62（'b'，和终端流的 0x74 区分）、u8 版本 1、u8 操作、u8 0、u32 编号（小端）、内容。
// 没声明这个能力的连接（旧版客户端）照旧：[]byte 由 encoding/json 编成标准 base64。
const (
	bulkMagic   = 0x62
	bulkVersion = 1
	bulkHeader  = 8

	opBlobChunk   = 1 // 电脑 → 手机：回复里一个字段的一块
	opUploadChunk = 2 // 手机 → 电脑：上传的一段
	opUploadAck   = 3 // 电脑 → 手机：上传已收下的字节数

	bulkChunk = 64 << 10
)

func bulkFrame(op byte, id uint32, payload []byte) []byte {
	b := make([]byte, bulkHeader, bulkHeader+len(payload))
	b[0], b[1], b[2] = bulkMagic, bulkVersion, op
	binary.LittleEndian.PutUint32(b[4:], id)
	return append(b, payload...)
}

// parseBulk 解开手机发来的二进制帧，不是大块帧时 ok 为 false。
func parseBulk(p []byte) (op byte, id uint32, payload []byte, ok bool) {
	if len(p) < bulkHeader || p[0] != bulkMagic || p[1] != bulkVersion {
		return 0, 0, nil, false
	}
	return p[2], binary.LittleEndian.Uint32(p[4:]), p[bulkHeader:], true
}

// replyBulk 回复一个可能带大块字段的结果。
func (c *conn) replyBulk(id string, res any, err error) {
	if err != nil || !c.hasCapability("posse.bulk.v1") {
		c.replyOrFail(id, res, err)
		return
	}
	m, ok := res.(map[string]any)
	if !ok {
		c.replyOrFail(id, res, nil)
		return
	}
	blobs := map[string]uint32{}
	if c.sendBlobs(m, "", blobs) != nil {
		return // 写失败时整条连接已经作废
	}
	if len(blobs) > 0 {
		m["blobs"] = blobs
	}
	c.reply(id, m)
}

// sendBlobs 把 m 里非空的 []byte 字段按块发出去，从 m 里摘掉，编号记进 blobs。
func (c *conn) sendBlobs(m map[string]any, prefix string, blobs map[string]uint32) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch v := m[k].(type) {
		case []byte:
			if len(v) == 0 {
				continue
			}
			bid := c.nextBlob.Add(1)
			for off := 0; off < len(v); off += bulkChunk {
				if err := c.sendBinary(bulkFrame(opBlobChunk, bid, v[off:min(off+bulkChunk, len(v))])); err != nil {
					return err
				}
			}
			delete(m, k)
			blobs[prefix+k] = bid
		case map[string]any:
			if err := c.sendBlobs(v, prefix+k+".", blobs); err != nil {
				return err
			}
		}
	}
	return nil
}

// ackUpload 记下 blob 收到了多少，交给后台发确认：读循环不等发送锁（可能排在别的大回复后面）。
// 确认是累计值，积压时只发最新的。调用方持有 smu。
func (c *conn) ackUpload(blob uint32, got int) {
	if c.acks == nil {
		c.acks = map[uint32]int{}
	}
	c.acks[blob] = got
	if c.acking {
		return
	}
	c.acking = true
	go c.sendAcks()
}

func (c *conn) sendAcks() {
	for {
		c.smu.Lock()
		var blob uint32
		var got int
		found := false
		for b, n := range c.acks {
			blob, got, found = b, n, true
			break
		}
		if !found {
			c.acking = false
			c.smu.Unlock()
			return
		}
		delete(c.acks, blob)
		c.smu.Unlock()
		var p [8]byte
		binary.LittleEndian.PutUint64(p[:], uint64(got))
		if c.sendBinary(bulkFrame(opUploadAck, blob, p[:])) != nil {
			// 连接已经作废，剩下的不用发了
			c.smu.Lock()
			c.acks = nil
			c.acking = false
			c.smu.Unlock()
			return
		}
	}
}
