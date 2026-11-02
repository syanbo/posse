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

/// 时刻，24 小时制：「18:04」。
String clockTime(AppLocalizations l, DateTime t) =>
    DateFormat.Hm(l.localeName).format(t);

/// 月日加时刻：「10月23日 18:04」「Oct 23 18:04」。
String monthDayTime(AppLocalizations l, DateTime t) =>
    DateFormat.MMMd(l.localeName).add_Hm().format(t);

/// 多久以前：刚刚、N 分钟前、N 小时前、N 天前。
String agoText(AppLocalizations l, DateTime t, DateTime now) {
  final d = now.difference(t);
  if (d < const Duration(minutes: 1)) return l.timeJustNow;
  if (d < const Duration(hours: 1)) return l.timeMinutesAgo(d.inMinutes);
  if (d < const Duration(days: 1)) return l.timeHoursAgo(d.inHours);
  return l.timeDaysAgo(d.inDays);
}
