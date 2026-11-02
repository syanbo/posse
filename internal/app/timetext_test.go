package app

import (
	"fmt"
	"testing"
	"time"

	"posse/locales"
)

// 相对时间按分钟、小时、天分档，英文和中文各看边界。
func TestAgoAndElapsed(t *testing.T) {
	en, zh := locales.In("en"), locales.In("zh-Hans")
	// 留出半分钟余量：测试跑的这一会儿不会跨档
	back := func(d time.Duration) time.Time { return time.Now().Add(-d - 30*time.Second) }
	cases := []struct {
		d                   time.Duration
		ago, elapsed, zhAgo string
	}{
		{-30 * time.Second, "Just now", "Just now", "刚刚"},
		{time.Minute, "1 min ago", "1 min", "1 分钟前"},
		{2 * time.Minute, "2 min ago", "2 min", "2 分钟前"},
		{59 * time.Minute, "59 min ago", "59 min", "59 分钟前"},
		{time.Hour, "1 hr ago", "1 hr", "1 小时前"},
		{47 * time.Hour, "47 hr ago", "47 hr", "47 小时前"},
		{48 * time.Hour, "2 days ago", "2 days", "2 天前"},
		{400 * 24 * time.Hour, "400 days ago", "400 days", "400 天前"},
	}
	for _, tc := range cases {
		at := back(tc.d)
		if got := ago(en, at); got != tc.ago {
			t.Errorf("ago(%v) = %q, want %q", tc.d, got, tc.ago)
		}
		if got := elapsed(en, at); got != tc.elapsed {
			t.Errorf("elapsed(%v) = %q, want %q", tc.d, got, tc.elapsed)
		}
		if got := ago(zh, at); got != tc.zhAgo {
			t.Errorf("zh ago(%v) = %q, want %q", tc.d, got, tc.zhAgo)
		}
	}
	if got := agoInline(en, time.Now()); got != "just now" {
		t.Errorf("agoInline now = %q", got)
	}
	if got := agoInline(en, back(5*time.Minute)); got != "5 min ago" {
		t.Errorf("agoInline 5 min = %q", got)
	}
	if got := elapsed(zh, back(3*time.Hour)); got != "3 小时" {
		t.Errorf("zh elapsed = %q", got)
	}
}

// 日期分组、操作记录的日期标题、提交时间：今天、昨天、今年、跨年。
func TestDayTextsEnglish(t *testing.T) {
	en, zh := locales.In("en"), locales.In("zh-Hans")
	now := time.Now()
	at := func(d time.Time, h, m int) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), h, m, 5, 0, time.Local)
	}
	today, yesterday := at(now, 0, 1), at(now.AddDate(0, 0, -1), 23, 59)
	lastYear := time.Date(now.Year()-1, 12, 30, 9, 2, 0, 0, time.Local)

	if got := consoleDay(en, today); got != "Today" {
		t.Errorf("consoleDay today %q", got)
	}
	if got := consoleDay(en, yesterday); got != "Yesterday" {
		t.Errorf("consoleDay yesterday %q", got)
	}
	if got := consoleDay(en, lastYear); got != fmt.Sprintf("Dec 30, %d", now.Year()-1) {
		t.Errorf("consoleDay last year %q", got)
	}
	if got := consoleDay(zh, lastYear); got != fmt.Sprintf("%d年12月30日", now.Year()-1) {
		t.Errorf("zh consoleDay last year %q", got)
	}

	if got := commitTime(en, today); got != "Today 00:01" {
		t.Errorf("commitTime today %q", got)
	}
	if got := commitTime(en, yesterday); got != "Yesterday 23:59" {
		t.Errorf("commitTime yesterday %q", got)
	}
	if got := commitTime(en, lastYear); got != fmt.Sprintf("%d-12-30", now.Year()-1) {
		t.Errorf("commitTime last year %q", got)
	}
	if now.YearDay() > 3 {
		jan1 := time.Date(now.Year(), 1, 1, 9, 2, 0, 0, time.Local)
		if got := commitTime(en, jan1); got != "Jan 1 09:02" {
			t.Errorf("commitTime this year %q", got)
		}
		if got := consoleDay(en, jan1); got != "Jan 1" {
			t.Errorf("consoleDay this year %q", got)
		}
		if got := consoleDay(zh, jan1); got != "1月1日" {
			t.Errorf("zh consoleDay this year %q", got)
		}
	}
	if got := commitStamp(en, time.Date(2026, 10, 14, 17, 17, 5, 0, time.Local)); got != "2026-10-14 17:17:05 Wed" {
		t.Errorf("commitStamp %q", got)
	}

	groups := map[time.Time]string{today: "Today", yesterday: "Yesterday", lastYear: "Older",
		now.AddDate(0, 0, -5): "Previous 7 Days", now.AddDate(0, 0, -20): "Previous 30 Days"}
	for d, want := range groups {
		if got := dayGroupText(en, dayGroup(d)); got != want {
			t.Errorf("dayGroup(%v) = %q, want %q", d, got, want)
		}
	}
	if got := dayGroupText(zh, dayGroup(lastYear)); got != "1 个月前" {
		t.Errorf("zh dayGroup %q", got)
	}
}

// 套餐窗口的重置时间：已过、不到一小时、整小时、小时加分钟、超过一天写日期。
func TestResetText(t *testing.T) {
	en, zh := locales.In("en"), locales.In("zh-Hans")
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.Local)
	ms := func(d time.Duration) int64 { return now.Add(d).UnixMilli() }
	cases := []struct {
		d      time.Duration
		en, zh string
	}{
		{-time.Minute, "Already reset", "已重置"},
		{30 * time.Second, "Resets in 1 min", "1 分钟后重置"},
		{58*time.Minute + 30*time.Second, "Resets in 59 min", "59 分钟后重置"},
		{2 * time.Hour, "Resets in 2 hr", "2 小时后重置"},
		{2*time.Hour + 15*time.Minute, "Resets in 2 hr 15 min", "2 小时 15 分钟后重置"},
		{30*time.Hour + 4*time.Minute, "Resets Oct 23 18:04", "10月23日 18:04 重置"},
	}
	for _, tc := range cases {
		if got := resetText(en, ms(tc.d), now); got != tc.en {
			t.Errorf("resetText(%v) = %q, want %q", tc.d, got, tc.en)
		}
		if got := resetText(zh, ms(tc.d), now); got != tc.zh {
			t.Errorf("zh resetText(%v) = %q, want %q", tc.d, got, tc.zh)
		}
	}
	if resetText(en, 0, now) != "" {
		t.Error("no reset time should be empty")
	}
}

// 日期和时间的中文和原来的 Go 格式一字不差。
func TestDateTextsKeepChinese(t *testing.T) {
	zh := locales.In("zh-Hans")
	for _, at := range []time.Time{time.Date(2026, 1, 2, 9, 4, 5, 0, time.Local), time.Date(2026, 12, 30, 23, 59, 59, 0, time.Local)} {
		if got, want := zh.AppDate(at), at.Format("1月2日"); got != want {
			t.Errorf("AppDate %q, want %q", got, want)
		}
		if got, want := zh.AppDateTime(at), at.Format("1月2日 15:04"); got != want {
			t.Errorf("AppDateTime %q, want %q", got, want)
		}
		if got, want := zh.AppDateTimeSeconds(at), at.Format("1月2日 15:04:05"); got != want {
			t.Errorf("AppDateTimeSeconds %q, want %q", got, want)
		}
	}
}
