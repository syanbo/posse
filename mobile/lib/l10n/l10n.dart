import 'package:flutter/widgets.dart';

import 'app_localizations.dart';

export 'app_localizations.dart';
export 'dates.dart';

/// 界面文字：`context.l10n.settingsLanguageTitle`。没有译文表时（测试里自己建的 MaterialApp）用中文，
/// 测试照旧按中文找元素；App 里 MaterialApp 总带着译文表。
extension L10nContext on BuildContext {
  AppLocalizations get l10n =>
      Localizations.of<AppLocalizations>(this, AppLocalizations) ??
      lookupAppLocalizations(const Locale('zh'));
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
