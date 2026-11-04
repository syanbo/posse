package desk

import "time"

// passwordGap 内打完过一条密码，再来的密码整条丢掉：loginwindow 还在验证上一条，
// 这时打进去的字会接在后面，多算一次输错。手机连点「解锁」时会连发几条。
const passwordGap = 2 * time.Second

// passwordGate 挡住紧跟在上一条后面的密码。
type passwordGate struct{ last time.Time }

func (g *passwordGate) allow(now time.Time) bool {
	return g.last.IsZero() || now.Sub(g.last) >= passwordGap
}

func (g *passwordGate) typed(now time.Time) { g.last = now }

// lockScreen 是输锁屏密码用到的系统接口，测试里换成假的。
type lockScreen interface {
	// locked 当场查 Mac 是否锁着屏，不看锁屏通知更新的缓存：通知晚到时缓存还是锁着
	locked() bool
	// target 是锁屏界面所在的进程
	target() (pid int32, ok bool)
	// ready 在密码框可能收起了时唤出它，等它能收字
	ready()
	// press 把一个字按下再松开，只投给 pid
	press(pid int32, r rune) bool
	// enter 按一下回车，只投给 pid
	enter(pid int32) bool
}

// typePassword 在锁屏的密码框里输入 text 再回车，返回是否整串打完、按了回车：只有这时手机才可以记住这串密码。
// 按键只投给锁屏界面的进程，不经 HID 层：解锁那一刻还没发出的按键在 HID 层会落进最前面的 App。
// 逐字之间和回车之前都再查一次锁屏，解开了就停。日志里不写密码和它的长度。
func typePassword(ls lockScreen, text string, logf func(string, ...any)) bool {
	if text == "" {
		return false
	}
	if !ls.locked() {
		logf("password dropped: screen is not locked")
		return false
	}
	pid, ok := ls.target()
	if !ok {
		logf("password dropped: lock screen process not found")
		return false
	}
	ls.ready()
	for _, r := range text {
		if !ls.locked() {
			logf("password stopped: screen unlocked while typing")
			return false
		}
		if !ls.press(pid, r) {
			logf("password stopped: key event failed")
			return false
		}
	}
	if !ls.locked() {
		logf("password stopped: screen unlocked before return")
		return false
	}
	if !ls.enter(pid) {
		logf("password stopped: key event failed")
		return false
	}
	return true
}
