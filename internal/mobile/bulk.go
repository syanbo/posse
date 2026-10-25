package mobile

import (
	"encoding/binary"
	"sort"
)

// 大块数据走二进制帧（Hive 扩展 hive.bulk.v1）。文本帧是 JSON 加密后再整帧 base64，
// 回复里的文件内容本身又是 base64，体积约为原始数据的 1.78 倍；整条消息还一直占着发送锁，
// 按键回显和 ping 的回复只能排在后面。
//
// 电脑 → 手机：回复里的 []byte 字段拆成 bulkChunk 大小的块先发，每块单独拿一次发送锁，
// 别的消息能插在块之间；最后发 JSON 回复，字段换成 "blobs": {"字段路径": 块编号}（嵌套的用点连起来）。
// 同一条连接按顺序到达，手机收到回复时这些块都已经到了。
// 手机 → 电脑：上传的分段（clipboard.startImageUpload 带 bytes 时）也走二进制帧，不用每段等一次回复。
//
// 帧：u8 0x62（'b'，和 Orca 终端流的 0x74 区分）、u8 版本 1、u8 操作、u8 0、u32 编号（小端）、内容。
// 没声明这个能力的连接（Orca App）照旧：[]byte 由 encoding/json 编成标准 base64，和以前一样。
const (
	bulkMagic   = 0x62
	bulkVersion = 1
	bulkHeader  = 8

	opBlobChunk   = 1 // 电脑 → 手机：回复里一个字段的一块
	opUploadChunk = 2 // 手机 → 电脑：上传的一段

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
	if err != nil || !c.hasCapability("hive.bulk.v1") {
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
