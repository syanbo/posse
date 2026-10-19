import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/ui/theme.dart';
import 'package:hive_mobile/workspace/models.dart';
import 'package:hive_mobile/workspace/session_actions.dart';

void main() {
  testWidgets('更多从底部滑上来：切换、电脑布局开关、关闭会话', (tester) async {
    const tab = SessionTab(
      worktreeId: 'p::w',
      title: 'demo',
      terminalId: 't1',
      agent: 'claude',
    );
    String? action;
    final flips = <bool>[];
    await tester.pumpWidget(
      MaterialApp(
        home: Builder(
          builder: (context) => TextButton(
            onPressed: () async {
              action = await showSessionMore(
                context,
                tab: tab,
                switchLabel: '切换到对话',
                desk: false,
                onDesk: flips.add,
              );
            },
            child: const Text('更多'),
          ),
        ),
      ),
    );
    await tester.tap(find.text('更多'));
    await tester.pump();
    // 有过渡：刚点下去时面板还没到位
    final start = tester.getTopLeft(find.text('关闭会话')).dy;
    await tester.pumpAndSettle();
    expect(tester.getTopLeft(find.text('关闭会话')).dy, lessThan(start));

    await tester.tap(find.text('电脑布局'));
    await tester.pumpAndSettle();
    expect(flips, [true]);
    expect(find.text('切换到对话'), findsOneWidget, reason: '拨开关面板不收');

    final sw = tester.widget<Switch>(find.byType(Switch));
    expect(sw.activeTrackColor, HiveColors.light.accent);
    final scales = tester
        .widgetList<Transform>(
          find.ancestor(
            of: find.byType(Switch),
            matching: find.byType(Transform),
          ),
        )
        .map((t) => t.transform.entry(0, 0));
    expect(scales, contains(closeTo(0.8, 0.001)));

    await tester.tap(find.text('切换到对话'));
    await tester.pumpAndSettle();
    expect(action, 'switch');
  });

  testWidgets('拨电脑布局引起外层重建，开关仍跟着拨、能拨回去', (tester) async {
    const tab = SessionTab(
      worktreeId: 'p::w',
      title: 'demo',
      terminalId: 't1',
      agent: 'claude',
    );
    // 和真机一样：改设置会让整个 App 换主题重建
    final dark = ValueNotifier(false);
    final flips = <bool>[];
    await tester.pumpWidget(
      ValueListenableBuilder<bool>(
        valueListenable: dark,
        builder: (context, isDark, _) => MaterialApp(
          theme: isDark ? ThemeData.dark() : ThemeData.light(),
          home: Builder(
            builder: (context) => TextButton(
              onPressed: () => showSessionMore(
                context,
                tab: tab,
                desk: false,
                onDesk: (value) {
                  flips.add(value);
                  dark.value = !dark.value;
                },
              ),
              child: const Text('更多'),
            ),
          ),
        ),
      ),
    );
    await tester.tap(find.text('更多'));
    await tester.pumpAndSettle();

    bool switchOn() => tester.widget<Switch>(find.byType(Switch)).value;
    await tester.tap(find.text('电脑布局'));
    await tester.pumpAndSettle();
    expect(switchOn(), isTrue);
    await tester.tap(find.text('电脑布局'));
    await tester.pumpAndSettle();
    expect(switchOn(), isFalse);
    expect(flips, [true, false]);
  });
}
