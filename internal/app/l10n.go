package app

import (
	"cmp"
	"strings"
	"sync/atomic"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/locales"
)

// L 是视图里取界面文字的地方：按这一帧的语言（c.Locale()）。生成的平面函数（locales.SettingsTitle()）
// 读 App 全局的语言，离屏预览和单元测试里用 Tester.SetLocale 换的只是 c.Locale()，两者对不上；
// 平面函数只留给没有 Context 的地方（菜单、通知），TestNoFlatMessages 检查。
// 钟点按这一帧的时制（c.Region().HourCycle，跟系统「24 小时制」的开关）写，见 clockLocale。
func L(c *ui.Context) locales.Messages {
	return locales.In(clockLocale(c.Locale(), c.Region().HourCycle))
}

// appHourCycle 是系统的时制（12 或 24），给没有 Context 的 appL 用；窗口建好前、单元测试里是 0，按 24 小时。
var appHourCycle atomic.Int32

// clockLocale 给界面语言加上时制的 Unicode 扩展（-u-hc-h12 / -u-hc-h23）：译文里的钟点写成骨架 ::jm、::jms，
// 按这个扩展出 3:04 PM、下午3:04 或 15:04。不加扩展时 j 用语言自己的习惯（英文是 12 小时），
// 所以凡是取文字都要经这里；时制不是 12 一律按 24 小时，Tester 的 Region 是零值，测试里固定 24 小时。
func clockLocale(lang string, hourCycle int) string {
	if lang == "" {
		lang = "en"
	}
	hc := "h23"
	if hourCycle == 12 {
		hc = "h12"
	}
	if i := strings.Index(lang, "-u-"); i >= 0 {
		if strings.Contains(lang[i:], "-hc-") {
			return lang
		}
		return lang + "-hc-" + hc
	}
	return lang + "-u-hc-" + hc
}

// appL 按 App 当前的语言取文字，只给没有 Context、也没有视图调用方的地方用：系统通知、轮询线程里发的提示和报错。
// 视图和由视图触发的操作一律用 L(c) 或调用方传进来的 Messages。
// 用 i18n.Locale 而不是 mygo.App.Locale：后者在 App 没跑起来时（单元测试）会一直等主线程。
func appL() locales.Messages {
	return locales.In(clockLocale(i18n.Locale(), int(appHourCycle.Load())))
}

// startLocale 在窗口建好后读出用户选的语言（设置页显示），并在语言变了时重装菜单：mygo 只重贴系统角色的菜单项，
// installMenu 自己加的项和 File、View、Window 的标题是装的时候按当时的语言取的。
func (a *app) startLocale() {
	a.locale = mygo.App.ChosenLocale()
	setErrLang(mygo.App.Locale())
	appHourCycle.Store(int32(mygo.App.Region().HourCycle))
	mygo.App.OnRegionChanged(func(r mygo.Region) { appHourCycle.Store(int32(r.HourCycle)) })
	mygo.App.OnLocaleChanged(func(locale string) {
		setErrLang(locale)
		a.installMenu()
	})
}

// setLocale 是设置页选了语言："" 跟随系统。mygo 存在 app 的 defaults 里，下次启动照用。
func (a *app) setLocale(tag string) {
	a.locale = tag
	mygo.App.SetLocale(tag)
}

// setErrLang 让报错的译文表跟着界面语言：中文用 zh 表，其余都显示英文原文。
func setErrLang(locale string) {
	if i18n.Base(locale) == "zh" {
		errtext.Current = errtext.ZH
	} else {
		errtext.Current = errtext.EN
	}
}

// languageSettings 是语言卡片：跟随系统，或 Posse 写了的语言之一，各用自己的名字（i18n.Autonym）。
func (a *app) languageSettings(c *ui.Context) {
	m := L(c)
	langs := mygo.App.Languages()
	labels := []string{m.SettingsLanguageSystem()}
	sel := 0
	chosen := ""
	if a.locale != "" {
		chosen = i18n.Match([]string{a.locale}, langs)
	}
	for i, tag := range langs {
		labels = append(labels, cmp.Or(i18n.Autonym(tag), tag))
		if tag == chosen {
			sel = i + 1
		}
	}
	settingsCard(c, m.SettingsLanguageTitle(), m.SettingsLanguageNote(), func() {
		if segmented(c, &sel, m.SettingsLanguageTitle(), labels...) {
			tag := ""
			if sel > 0 {
				tag = langs[sel-1]
			}
			a.setLocale(tag)
		}
	})
}
