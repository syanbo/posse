import 'package:flutter/widgets.dart';
import 'package:intl/intl.dart';

import 'app_localizations.dart';

// 日期按译文表的语言（l.localeName）格式化，和界面文字同一种语言：测试里没有译文表时 context.l10n
// 退回中文，日期也跟着中文，不会一半英文。intl 的日期数据由 flutter_localizations 在载入译文表时登记。

/// 月日：「10月23日」「Oct 23」。
String monthDay(AppLocalizations l, DateTime t) =>
    DateFormat.MMMd(l.localeName).format(t);

/// 带年份的月日：「2025年9月3日」「Sep 3, 2025」。
String yearMonthDay(AppLocalizations l, DateTime t) =>
    DateFormat.yMMMd(l.localeName).format(t);

/// 系统的时制：iOS、安卓设置里的「24 小时制」开关。钟点都按它写，没有 context 的函数由调用方传进去。
bool use24Hour(BuildContext context) =>
    MediaQuery.alwaysUse24HourFormatOf(context);

/// 时刻：24 小时制「18:04」；12 小时制「6:04 PM」「下午6:04」。
String clockTime(AppLocalizations l, DateTime t, {required bool h24}) =>
    _clock(l.localeName, h24).format(t).replaceAll('\u202f', ' ');

/// 月日加时刻：「10月23日 18:04」「Oct 23 18:04」；12 小时制「10月23日 下午6:04」「Oct 23 6:04 PM」。
String monthDayTime(AppLocalizations l, DateTime t, {required bool h24}) =>
    '${monthDay(l, t)} ${clockTime(l, t, h24: h24)}';

// 英文的 jm 在 PM 前用窄不换行空格，换成普通空格，和桌面一样。中文的 jm 是 24 小时（中文习惯），
// 12 小时制用 CLDR 的 hm 写法「ah:mm」。
DateFormat _clock(String locale, bool h24) {
  if (h24) return DateFormat.Hm(locale);
  final f = DateFormat.jm(locale);
  return f.pattern!.contains('H') ? DateFormat('ah:mm', locale) : f;
}

/// 多久以前：刚刚、N 分钟前、N 小时前、N 天前。
String agoText(AppLocalizations l, DateTime t, DateTime now) {
  final d = now.difference(t);
  if (d < const Duration(minutes: 1)) return l.timeJustNow;
  if (d < const Duration(hours: 1)) return l.timeMinutesAgo(d.inMinutes);
  if (d < const Duration(days: 1)) return l.timeHoursAgo(d.inHours);
  return l.timeDaysAgo(d.inDays);
}
