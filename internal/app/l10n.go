package app

import (
	"cmp"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/locales"
)

// L 是视图里取界面文字的地方：按这一帧的语言（c.Locale()）。生成的平面函数（locales.SettingsTitle()）
// 读 App 全局的语言，离屏预览和单元测试里用 Tester.SetLocale 换的只是 c.Locale()，两者对不上；
// 平面函数只留给没有 Context 的地方（菜单、通知），TestNoFlatMessages 检查。
func L(c *ui.Context) locales.Messages { return locales.In(c.Locale()) }

// startLocale 在窗口建好后读出用户选的语言（设置页显示），并在语言变了时重装菜单：mygo 只重贴系统角色的菜单项，
// installMenu 自己加的项和 File、View、Window 的标题是装的时候按当时的语言取的。
func (a *app) startLocale() {
	a.locale = mygo.App.ChosenLocale()
	setErrLang(mygo.App.Locale())
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
