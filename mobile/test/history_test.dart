import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/history/models.dart';
import 'package:flutter/widgets.dart' show Locale;
import 'package:posse_mobile/l10n/l10n.dart';

final zh = lookupAppLocalizations(const Locale('zh'));
final en = lookupAppLocalizations(const Locale('en'));

void main() {
  test('parses list and search results', () {
    final list = parseHistory({
      'sessions': [
        {
          'agent': 'claude',
          'id': 's1',
          'title': ' 修登录 ',
          'cwd': '/Users/x/Apps/posse',
          'branch': 'main',
          'updatedAt': '2026-10-18T10:00:00Z',
          'messages': 12,
          'path': '/p/s1.jsonl',
        },
        {'agent': 'codex'}, // 没有 ID 的不要
      ],
    });
    expect(list, hasLength(1));
    expect(list.single.title, '修登录');
    expect(list.single.folder, 'posse');
    expect(list.single.updatedAt!.isUtc, isFalse);
    final hits = parseHistory({
      'hits': [
        {'agent': 'grok', 'id': 'g', 'snippet': '  命中的那句 ', 'matches': 3},
      ],
    });
    expect(hits.single.snippet, '命中的那句');
    expect(hits.single.matches, 3);
  });

  test('groups by day like the desktop history page', () {
    final now = DateTime(2026, 10, 18, 9);
    HistorySession at(DateTime t) => HistorySession(
      agent: 'claude',
      id: '$t',
      title: '',
      cwd: '',
      branch: '',
      updatedAt: t,
      messages: 0,
      path: '',
    );
    final groups = groupHistory(zh, [
      at(DateTime(2026, 10, 18, 8)),
      at(DateTime(2026, 10, 17, 23)),
      at(DateTime(2026, 10, 14)),
      at(DateTime(2026, 10, 1)),
      at(DateTime(2026, 8, 1)),
    ], now);
    expect(groups.map((g) => g.$1), ['今天', '昨天', '最近 7 天', '最近 30 天', '1 个月前']);
  });

  test('time label', () {
    final now = DateTime(2026, 10, 18, 9, 30);
    expect(historyTime(zh, null, now), '');
    expect(historyTime(zh, DateTime(2026, 10, 18, 9, 29, 30), now), '刚刚');
    expect(historyTime(zh, DateTime(2026, 10, 18, 9, 10), now), '20 分钟前');
    expect(historyTime(zh, DateTime(2026, 10, 18, 7, 5), now), '07:05');
    expect(historyTime(zh, DateTime(2026, 9, 3), now), '9月3日');
    expect(historyTime(zh, DateTime(2025, 9, 3), now), '2025年9月3日');
    expect(historyTime(en, DateTime(2026, 10, 18, 9, 29, 30), now), 'Just now');
    expect(historyTime(en, DateTime(2026, 10, 18, 9, 10), now), '20 min ago');
    expect(historyTime(en, DateTime(2026, 10, 18, 7, 5), now), '07:05');
    expect(historyTime(en, DateTime(2026, 9, 3), now), 'Sep 3');
    expect(historyTime(en, DateTime(2025, 9, 3), now), 'Sep 3, 2025');
  });

  test('ago text', () {
    final now = DateTime(2026, 10, 18, 9, 30);
    String ago(AppLocalizations l, Duration d) =>
        agoText(l, now.subtract(d), now);
    expect(ago(zh, const Duration(seconds: 59)), '刚刚');
    expect(ago(zh, const Duration(minutes: 1)), '1 分钟前');
    expect(ago(zh, const Duration(minutes: 59)), '59 分钟前');
    expect(ago(zh, const Duration(hours: 1)), '1 小时前');
    expect(ago(zh, const Duration(hours: 23, minutes: 59)), '23 小时前');
    expect(ago(zh, const Duration(days: 3)), '3 天前');
    expect(ago(en, const Duration(seconds: 59)), 'Just now');
    expect(ago(en, const Duration(minutes: 1)), '1 min ago');
    expect(ago(en, const Duration(hours: 1)), '1 hr ago');
    expect(ago(en, const Duration(hours: 5)), '5 hr ago');
    expect(ago(en, const Duration(days: 1)), '1 day ago');
    expect(ago(en, const Duration(days: 3)), '3 days ago');
  });
}
