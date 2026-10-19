package usage

import (
	"errors"
	"testing"
)

// 读一项不存在的密码：不弹授权窗，走完 purego 调 Security 框架的整条路。
func TestKeychainMissing(t *testing.T) {
	_, err := keychainPassword("hive-test-no-such-service", "nobody")
	if !errors.Is(err, ErrCursorDenied) {
		t.Fatalf("got %v", err)
	}
}
