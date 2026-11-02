package app

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"io"
	"os"
)

// releaseTeam 是发布 Posse 的证书所属团队（mygo.json 的 macos.signingIdentity）。只有它签的包才查
// posse.shaoyan.uk 的更新：从源码打的包（临时签名或别的证书）也带着 mygo.json 的更新地址，
// 放进「应用程序」后会被官方版本自动换掉。用自己的证书和更新地址发布修改版时改这里。
const releaseTeam = "QKSM9Z4U85"

// signingTeam 读可执行文件代码签名里的团队 ID；临时签名、没签名或读不了时为空串。
// 只认签名里写的，不校验签名（校验要调 Security 框架，这里只用来决定查不查更新）。
func signingTeam(exe string) string {
	f, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer f.Close()
	var m *macho.File
	if fat, err := macho.NewFatFile(f); err == nil {
		for _, a := range fat.Arches {
			if a.Cpu == macho.CpuArm64 {
				m = a.File
			}
		}
		if m == nil && len(fat.Arches) > 0 {
			m = fat.Arches[0].File
		}
		if m == nil {
			return ""
		}
		// fat 里每个架构的签名偏移相对它自己的切片
		for _, a := range fat.Arches {
			if a.File == m {
				return codeSignTeam(m, io.NewSectionReader(f, int64(a.Offset), int64(a.Size)))
			}
		}
	}
	if m, err = macho.NewFile(f); err != nil {
		return ""
	}
	return codeSignTeam(m, f)
}

const (
	lcCodeSignature   = 0x1d
	csSuperBlob       = 0xfade0cc0
	csCodeDirectory   = 0xfade0c02
	csSupportsTeamID  = 0x20200
	cdTeamOffsetField = 48
)

func codeSignTeam(m *macho.File, r io.ReaderAt) string {
	for _, l := range m.Loads {
		raw := l.Raw()
		if len(raw) < 16 || m.ByteOrder.Uint32(raw) != lcCodeSignature {
			continue
		}
		off, size := m.ByteOrder.Uint32(raw[8:]), m.ByteOrder.Uint32(raw[12:])
		if size > 16<<20 {
			return ""
		}
		blob := make([]byte, size)
		if _, err := r.ReadAt(blob, int64(off)); err != nil {
			return ""
		}
		return superBlobTeam(blob)
	}
	return ""
}

// superBlobTeam 在签名的 SuperBlob（大端）里找第 0 号槽的 CodeDirectory，取它的团队 ID。
func superBlobTeam(b []byte) string {
	be := binary.BigEndian
	if len(b) < 12 || be.Uint32(b) != csSuperBlob {
		return ""
	}
	count := be.Uint32(b[8:])
	for i := uint32(0); i < count; i++ {
		at := 12 + 8*int(i)
		if at+8 > len(b) {
			return ""
		}
		if be.Uint32(b[at:]) != 0 {
			continue
		}
		cd := int(be.Uint32(b[at+4:]))
		if cd+cdTeamOffsetField+4 > len(b) || be.Uint32(b[cd:]) != csCodeDirectory ||
			be.Uint32(b[cd+8:]) < csSupportsTeamID {
			return ""
		}
		team := int(be.Uint32(b[cd+cdTeamOffsetField:]))
		if team == 0 || cd+team >= len(b) {
			return ""
		}
		s := b[cd+team:]
		if end := bytes.IndexByte(s, 0); end >= 0 {
			return string(s[:end])
		}
		return ""
	}
	return ""
}
