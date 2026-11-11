package skills

import (
	"crypto/sha256"
	"encoding/hex"
)

// posseDigest 是自带技能内容的摘要，测试拿它核对改了内容有没有加版本。
func posseDigest() string {
	h := sha256.New()
	for _, s := range PosseSkills() {
		h.Write([]byte(s.Name + "\x00" + s.Content + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
