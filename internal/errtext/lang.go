package errtext

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"posse/internal/spawn"
)

// LangOf 把 BCP 47 的语言标签（zh-Hans、en-US）换成有译文表的界面语言：中文的各种写法是中文，
// 空的按中文（没报语言的手机），别的语言都退回英文源文本。
func LangOf(tag string) Lang {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" || tag == "zh" || strings.HasPrefix(tag, "zh-") || strings.HasPrefix(tag, "zh_") {
		return ZH
	}
	return EN
}

// SystemLang 是常驻进程自己显示的文字（看桌面的默认人名、恢复终端时打的一行）用的语言，常驻进程没有界面语言：
// 用户在 Posse 设置里选的语言优先，mygo 的 SetLocale 把它存在 app 自己的 defaults 里（包 ID 的 AppleLanguages，
// 跟随系统时没有这一项）；没选或不是从 .app 运行（go build 的 bin/posse）时按 macOS 的首选语言，
// 和窗口一样取列表里第一个有译文的语言（[ja, zh-Hans] 是中文），一个都没有时是英文；都读不到时是中文。
// 用户换了语言不用重启常驻进程：结果只缓存几秒，恢复一批终端时不用每个都起 defaults。
func SystemLang() Lang {
	sysLang.mu.Lock()
	defer sysLang.mu.Unlock()
	if !sysLang.at.IsZero() && time.Since(sysLang.at) < sysLangTTL {
		return sysLang.lang
	}
	sysLang.lang, sysLang.at = readSystemLang(), time.Now()
	return sysLang.lang
}

const sysLangTTL = 5 * time.Second

var sysLang struct {
	mu   sync.Mutex
	lang Lang
	at   time.Time
}

func readSystemLang() Lang {
	if exe, err := os.Executable(); err == nil {
		if id := spawn.BundleID(exe); id != "" {
			if tags := languages(id); len(tags) > 0 {
				return pickLang(tags)
			}
		}
	}
	return pickLang(languages("-g"))
}

// pickLang 取 tags 里第一个有译文的语言（英文是源文本，也算），都没有时是英文；tags 为空时是中文。
func pickLang(tags []string) Lang {
	if len(tags) == 0 {
		return ZH
	}
	for _, tag := range tags {
		if l := LangOf(tag); l == ZH || strings.HasPrefix(strings.ToLower(tag), "en") {
			return l
		}
	}
	return EN
}

// languages 读 defaults 域里的 AppleLanguages，没有时为空。
// 用 defaults 命令而不直接读 plist 文件：写入先进 cfprefsd 的缓存，文件不一定已经落盘。
func languages(domain string) []string {
	out, err := exec.Command("defaults", "read", domain, "AppleLanguages").Output()
	if err != nil {
		return nil
	}
	return parseLanguages(string(out))
}

// parseLanguages 取 defaults 打印的数组（`(\n    "zh-Hans-CN",\n    en\n)`）的各项。
func parseLanguages(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "(")
	var out []string
	for _, item := range strings.Split(s, ",") {
		item = strings.Trim(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(item), ")")), `"`)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
