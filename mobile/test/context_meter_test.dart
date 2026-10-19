import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/app_state.dart';
import 'package:hive_mobile/chat/context_meter.dart';
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
    expect(find.text('上下文 42%'), findsOneWidget);
    percent = 81;
    await show(_tab('claude', 'idle'));
    expect(reads, 2);
    expect(find.text('上下文 81%'), findsOneWidget);
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
    expect(find.textContaining('上下文'), findsNothing);
  });
}
