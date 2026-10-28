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
  ],
});

void main() {
  test('timeline merges consecutive calls, newest first', () {
    final steps = toolSteps(_usage.events);
    expect(
      [for (final s in steps) s.event.tool + s.event.cli + s.event.skill],
      ['phone_tap', 'git', 'phone_tap', 'archify'],
    );
    expect(steps[2].count, 2);
    expect(steps[2].first, DateTime.utc(2026, 10, 28, 1, 1).toLocal());
    expect(steps[2].event.at, DateTime.utc(2026, 10, 28, 1, 2).toLocal());
  });

  test('skills only drops command line programs and rejoins the rest', () {
    final steps = toolSteps(_usage.events, skillsOnly: true);
    expect(steps.length, 2);
    expect(steps.first.event.tool, 'phone_tap');
    expect(steps.first.count, 3);
  });

  Widget host(bool skillsOnly) => MaterialApp(
    theme: posseTheme(Brightness.light),
    home: Scaffold(
      body: ToolsView(usage: _usage, skillsOnly: skillsOnly),
    ),
  );

  testWidgets('groups command line programs by source', (tester) async {
    await tester.pumpWidget(host(false));
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
    await tester.pumpWidget(host(true));
    expect(find.text('Homebrew'), findsNothing);
    expect(find.text('git'), findsNothing);
    // 汇总里 iphone-use 的次数，和时间线里跳过 git 后合成一项的三次 phone_tap
    expect(find.text('×3'), findsNWidgets(2));
  });

  testWidgets('timeline time sits at the right edge', (tester) async {
    await tester.pumpWidget(host(false));
    final at = DateTime.utc(2026, 10, 28, 1, 3).toLocal();
    final hm =
        '${at.hour.toString().padLeft(2, '0')}:${at.minute.toString().padLeft(2, '0')}';
    final time = tester.getRect(find.text(hm).first);
    final view = tester.getRect(find.byType(ToolsView));
    // 页面左右各 16 的边距
    expect(time.right, closeTo(view.right - 16, 1));
  });
}
