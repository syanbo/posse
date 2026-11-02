package errtext

import (
	"os"
	"os/exec"
	"strings"

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
// 跟随系统时没有这一项）；没选或不是从 .app 运行（go build 的 bin/posse）时按 macOS 的首选语言；都读不到时是中文。
// 每次现读，用户换了语言不用重启常驻进程。
func SystemLang() Lang {
	if exe, err := os.Executable(); err == nil {
		if id := spawn.BundleID(exe); id != "" {
			if tag := firstLanguage(id); tag != "" {
				return LangOf(tag)
			}
		}
	}
	return LangOf(firstLanguage("-g"))
}

// firstLanguage 读 defaults 域里 AppleLanguages 的第一项，没有时为空。
// 用 defaults 命令而不直接读 plist 文件：写入先进 cfprefsd 的缓存，文件不一定已经落盘。
func firstLanguage(domain string) string {
	out, err := exec.Command("defaults", "read", domain, "AppleLanguages").Output()
	if err != nil {
		return ""
	}
	return parseLanguages(string(out))
}

// parseLanguages 取 defaults 打印的数组（`(\n    "zh-Hans-CN",\n    en\n)`）的第一项。
func parseLanguages(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "(")
	for _, item := range strings.Split(s, ",") {
		item = strings.Trim(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(item), ")")), `"`)
		if item != "" {
			return item
		}
	}
	return ""
}
