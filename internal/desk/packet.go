package desk

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	PacketHello    byte = 1
	PacketVideo    byte = 2
	PacketReport   byte = 3
	PacketInput    byte = 4
	PacketKeyframe byte = 5
	PacketBeat     byte = 6

	DirToMac    byte = 0
	DirToMobile byte = 1

	packetHead  = 1 + 8
	packetTag   = 16
	PacketLimit = 1200

	// 本机验收用的固定材料。接上配对之后换成握手密钥和订阅 nonce。
	testSecret = "hive-desk-test-secret-32bytes!!" // 32
	testNonce  = "hive-desk-nonce!"                // 16
)

var errPacket = errors.New("desk: bad packet")

// DeriveKeys 从共享密钥和会话 nonce 派生两个方向的 AES-256 密钥。
func DeriveKeys(secret, sessionNonce []byte) (toMobile, toMac [32]byte, err error) {
	out, err := hkdf.Key(sha256.New, secret, sessionNonce, "hive-desk/v1", 64)
	if err != nil {
		return toMobile, toMac, err
	}
	copy(toMobile[:], out[:32])
	copy(toMac[:], out[32:])
	return toMobile, toMac, nil
}

// TestKeys 是 desk cast 和 tools/deskrecv 共用的本机密钥。
func TestKeys() (toMobile, toMac [32]byte) {
	toMobile, toMac, err := DeriveKeys([]byte(testSecret), []byte(testNonce))
	if err != nil {
		panic(err)
	}
	return toMobile, toMac
}

// NewAEAD 按一把已派生的密钥建好 GCM。热路径复用这一个，不再每个包新建。
func NewAEAD(key [32]byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal 把明文打成一个数据报。counter 从 1 起。aead 可以反复用。
func Seal(aead cipher.AEAD, dir, kind byte, counter uint64, plain []byte) ([]byte, error) {
	payload := packetHead + len(plain) + packetTag
	if payload > PacketLimit {
		return nil, fmt.Errorf("desk: packet is %d bytes", payload)
	}
	out := make([]byte, packetHead, payload)
	out[0] = kind
	binary.BigEndian.PutUint64(out[1:9], counter)
	return aead.Seal(out, packetNonce(dir, counter), plain, out[:1]), nil
}

// Open 解开一个数据报，返回类型、计数器和明文。aead 可以反复用。
func Open(aead cipher.AEAD, dir byte, packet []byte) (kind byte, counter uint64, plain []byte, err error) {
	if len(packet) < packetHead+packetTag || len(packet) > PacketLimit {
		return 0, 0, nil, errPacket
	}
	plain, err = aead.Open(nil, packetNonce(dir, binary.BigEndian.Uint64(packet[1:9])), packet[packetHead:], packet[:1])
	if err != nil {
		return 0, 0, nil, errPacket
	}
	return packet[0], binary.BigEndian.Uint64(packet[1:9]), plain, nil
}

func packetNonce(dir byte, counter uint64) []byte {
	var nonce [12]byte
	nonce[0] = dir
	binary.BigEndian.PutUint64(nonce[4:], counter)
	return nonce[:]
}

// Replay 是 1024 位滑动窗口。乱序的收，重复的和落在窗口前的丢。
type Replay struct {
	last uint64
	bits [16]uint64
}

func (r *Replay) Accept(n uint64) bool {
	if n == 0 {
		return false
	}
	if r.last == 0 {
		r.last = n
		r.bits[0] = 1
		return true
	}
	if n > r.last {
		shift := n - r.last
		if shift >= 1024 {
			r.bits = [16]uint64{}
		} else {
			r.shift(shift)
		}
		r.last = n
		r.bits[0] |= 1
		return true
	}
	back := r.last - n
	if back >= 1024 {
		return false
	}
	i, b := back/64, back%64
	if r.bits[i]&(1<<b) != 0 {
		return false
	}
	r.bits[i] |= 1 << b
	return true
}

func (r *Replay) shift(n uint64) {
	words := n / 64
	bits := n % 64
	var next [16]uint64
	for i := range next {
		from := int(words) + i
		if from >= len(r.bits) {
			break
		}
		next[i] = r.bits[from] << bits
		if bits != 0 && from+1 < len(r.bits) {
			next[i] |= r.bits[from+1] >> (64 - bits)
		}
	}
	r.bits = next
}
