import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/chat/tools.dart';
import 'package:posse_mobile/ui/theme.dart';

final _usage = ToolUsage.fromJson({
  'found': true,
  'skills': [
    {'name': 'archify', 'count': 2},
  ],
  'mcp': [
    {
      'server': 'iphone-use',
      'count': 3,
      'tools': [
        {'name': 'phone_tap', 'count': 3},
      ],
    },
  ],
  'cli': [
    {'name': 'git', 'source': 'homebrew', 'count': 4},
    {'name': 'flutter', 'count': 1},
    {'name': 'xcrun', 'source': 'system', 'count': 1},
  ],
  'events': [
    {'at': '2026-10-28T01:00:00Z', 'skill': 'archify'},
    {'at': '2026-10-28T01:01:00Z', 'server': 'iphone-use', 'tool': 'phone_tap'},
    {'at': '2026-10-28T01:02:00Z', 'server': 'iphone-use', 'tool': 'phone_tap'},
    {'cli': 'git', 'source': 'homebrew', 'command': 'git status'},
    {'at': '2026-10-28T01:03:00Z', 'server': 'iphone-use', 'tool': 'phone_tap'},
    {
      'at': '2026-10-28T01:04:00Z',
      'url': 'https://www.github.com/a/b?x=1',
      'via': 'user',
    },
    {
      'at': '2026-10-28T01:05:00Z',
      'query': 'flutter sliver',
      'via': 'WebSearch',
    },
  ],
  'web': [
    {'host': 'github.com', 'count': 1},
  ],
  'searches': 1,
});

void main() {
  test('timeline merges consecutive calls, newest first', () {
    final steps = toolSteps(_usage.events);
    expect(
      [
        for (final s in steps)
          s.event.tool +
              s.event.cli +
              s.event.skill +
              s.event.url +
              s.event.query,
      ],
      [
        'flutter sliver',
        'https://www.github.com/a/b?x=1',
        'phone_tap',
        'git',
        'phone_tap',
        'archify',
      ],
    );
    expect(steps[4].count, 2);
    expect(steps[4].first, DateTime.utc(2026, 10, 28, 1, 1).toLocal());
    expect(steps[4].event.at, DateTime.utc(2026, 10, 28, 1, 2).toLocal());
  });

  test('skills only drops command line programs and rejoins the rest', () {
    final steps = toolSteps(_usage.events, filter: ToolsFilter.skills);
    expect(steps.length, 2);
    expect(steps.first.event.tool, 'phone_tap');
    expect(steps.first.count, 3);
  });

  test('web filter keeps only urls and searches', () {
    final steps = toolSteps(_usage.events, filter: ToolsFilter.web);
    expect(
      [for (final s in steps) s.event.query + s.event.url],
      ['flutter sliver', 'https://www.github.com/a/b?x=1'],
    );
    expect(webSite('https://www.github.com/a/b?x=1'), (
      'github.com',
      '/a/b?x=1',
    ));
  });

  Widget host(ToolsFilter filter) => MaterialApp(
    theme: posseTheme(Brightness.light),
    home: Scaffold(
      body: ToolsView(usage: _usage, filter: filter),
    ),
  );

  testWidgets('groups command line programs by source', (tester) async {
    await tester.pumpWidget(host(ToolsFilter.all));
    for (final t in [
      'Homebrew',
      '其他命令行',
      '系统自带',
      '时间线',
      'archify',
      'iphone-use',
    ]) {
      expect(find.text(t), findsWidgets, reason: t);
    }
    expect(find.text('×4'), findsOneWidget);

    await tester.tap(find.text('archify').first);
    await tester.pump();
    expect(find.text('已复制 archify'), findsOneWidget);
  });

  testWidgets('skills only hides command line groups', (tester) async {
    await tester.pumpWidget(host(ToolsFilter.skills));
    expect(find.text('Homebrew'), findsNothing);
    expect(find.text('git'), findsNothing);
    // 汇总里 iphone-use 的次数，和时间线里跳过 git 后合成一项的三次 phone_tap
    expect(find.text('×3'), findsNWidgets(2));
  });

  testWidgets('timeline time sits at the right edge', (tester) async {
    await tester.pumpWidget(host(ToolsFilter.all));
    final at = DateTime.utc(2026, 10, 28, 1, 5).toLocal();
    final hm =
        '${at.hour.toString().padLeft(2, '0')}:${at.minute.toString().padLeft(2, '0')}';
    final time = tester.getRect(find.text(hm).first);
    final view = tester.getRect(find.byType(ToolsView));
    // 页面左右各 16 的边距
    expect(time.right, closeTo(view.right - 16, 1));
  });

  testWidgets('web filter lists sites and copies the full url', (tester) async {
    await tester.pumpWidget(host(ToolsFilter.web));
    expect(find.text('Homebrew'), findsNothing);
    expect(find.text('archify'), findsNothing);
    expect(find.text('网址  搜索 1 次'), findsOneWidget);
    expect(find.text('你发的 · /a/b?x=1'), findsOneWidget);
    expect(find.text('搜索 · WebSearch'), findsOneWidget);

    await tester.tap(find.text('你发的 · /a/b?x=1'));
    await tester.pump();
    expect(find.text('已复制 https://www.github.com/a/b?x=1'), findsOneWidget);
  });
}
