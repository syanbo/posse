import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/app_state.dart';
import 'package:hive_mobile/chat/context_meter.dart';
import 'package:hive_mobile/chat/send.dart';
import 'package:hive_mobile/terminal/frames.dart';
import 'package:hive_mobile/workspace/models.dart';

SessionTab _tab(String agent, String state) => SessionTab(
  worktreeId: 'w',
  title: agent,
  terminalId: 't1',
  agent: agent,
  state: state,
  sessionId: 's1',
);

void main() {
  final model = HiveAppModel(
    store: MemoryPairingStore(),
    settingsStore: MemorySettingsStore(),
  );

  testWidgets('shows the context share and rereads when a turn ends', (
    tester,
  ) async {
    var reads = 0;
    var percent = 42;
    Future<void> show(SessionTab tab) async {
      await tester.pumpWidget(
        MaterialApp(
          home: Scaffold(
            body: ContextMeter(
              tab: tab,
              model: model,
              read: (_) async {
                reads++;
                return percent;
              },
            ),
          ),
        ),
      );
      await tester.pump();
    }

    await show(_tab('claude', 'working'));
    expect(find.text('42%'), findsOneWidget);
    // 和前面的状态文字同一个样式，写明字重，放进顶栏也不会继承标题的粗体
    expect(
      tester.widget<Text>(find.text('42%')).style!.fontWeight,
      FontWeight.w400,
    );
    percent = 81;
    await show(_tab('claude', 'idle'));
    expect(reads, 2);
    expect(find.text('81%'), findsOneWidget);
  });

  testWidgets('agents without usage records show nothing', (tester) async {
    var reads = 0;
    await tester.pumpWidget(
      MaterialApp(
        home: ContextMeter(
          tab: _tab('grok', 'idle'),
          model: model,
          read: (_) async {
            reads++;
            return 10;
          },
        ),
      ),
    );
    await tester.pump();
    expect(reads, 0);
    expect(find.textContaining('%'), findsNothing);
  });

  // 点用量问要不要压缩：取消什么也不发；确认后先清空输入框，再发 /compact 加回车
  testWidgets('tapping the meter asks, then clears the input and compacts', (
    tester,
  ) async {
    final sent = <List<int>>[];
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: ContextMeter(
            tab: _tab('claude', 'idle'),
            model: model,
            read: (_) async => 83,
            send: (tab, raw) async {
              expect(tab.terminalId, 't1');
              sent.add(raw);
              return SendOutcome.accepted;
            },
          ),
        ),
      ),
    );
    await tester.pump();
    await tester.tap(find.byKey(const Key('context-meter')));
    await tester.pumpAndSettle();
    expect(find.text('压缩上下文？'), findsOneWidget);
    await tester.tap(find.text('取消'));
    await tester.pumpAndSettle();
    expect(sent, isEmpty);

    await tester.tap(find.byKey(const Key('context-meter')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('压缩'));
    await tester.pumpAndSettle();
    await tester.pump(const Duration(seconds: 1));
    // 清空两次、命令、回车分开写，回车不跟着命令一起进去
    expect(sent, [
      clearLines(),
      clearLines(),
      '/compact'.codeUnits,
      [0x0d],
    ]);
  });
}
