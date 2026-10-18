import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/history/models.dart';

void main() {
  test('parses list and search results', () {
    final list = parseHistory({
      'sessions': [
        {
          'agent': 'claude',
          'id': 's1',
          'title': ' 修登录 ',
          'cwd': '/Users/x/Apps/hive',
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
    expect(list.single.folder, 'hive');
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
    final groups = groupHistory([
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
    expect(historyTime(DateTime(2026, 10, 18, 9, 10), now), '20 分钟前');
    expect(historyTime(DateTime(2026, 10, 18, 7, 5), now), '07:05');
    expect(historyTime(DateTime(2026, 9, 3), now), '9月3日');
    expect(historyTime(DateTime(2025, 9, 3), now), '2025年9月3日');
  });
}
