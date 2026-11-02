import 'package:flutter/widgets.dart';

import 'app_localizations.dart';

export 'app_localizations.dart';

/// 界面文字：`context.l10n.settingsLanguageTitle`。
extension L10nContext on BuildContext {
  AppLocalizations get l10n => AppLocalizations.of(this);
}

/// 设置里选的语言：空串跟随系统，否则是 [AppLocalizations.supportedLocales] 里的语言代码。
Locale? chosenLocale(String language) =>
    language.isEmpty ? null : Locale(language);

String _current = 'zh-Hans';

/// 当前的界面语言（BCP 47），连接电脑时随 clientCapabilities.update 报过去，电脑按它翻译报错和说明。
String posseLocale() => _current;

/// 界面定下语言后调用（MaterialApp 解析完系统语言和设置）；变了返回 true，调用方再报给电脑。
bool setPosseLocale(Locale locale) {
  final tag = locale.languageCode == 'zh' ? 'zh-Hans' : locale.toLanguageTag();
  if (tag == _current) return false;
  _current = tag;
  return true;
}

/// 设置里能选的语言，各用自己的名字（不随界面语言变），顺序就是设置里的顺序。
const languageNames = {'zh': '简体中文', 'en': 'English'};
