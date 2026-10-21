import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/app_state.dart';
import 'package:hive_mobile/chat/session_bar.dart';
import 'package:hive_mobile/ui/loading.dart';
import 'package:hive_mobile/ui/theme.dart';
import 'package:hive_mobile/workspace/models.dart';

const tab = SessionTab(
  worktreeId: 'r::w',
  title: '礼物Tab切换卡顿',
  terminalId: 't1',
  agent: 'claude',
  state: 'done',
);

Widget host(Widget title) => MaterialApp(
  theme: hiveTheme(Brightness.dark),
  home: Scaffold(appBar: AppBar(title: title)),
);

void main() {
  testWidgets('connected: the line under the title is the session state', (
    tester,
  ) async {
    await tester.pumpWidget(host(const SessionTitle(tab: tab)));
    expect(find.text('本轮完成'), findsOneWidget);
    expect(find.byType(HexSpinner), findsNothing);
  });

  testWidgets('disconnected: reconnect progress takes the state line', (
    tester,
  ) async {
    final model = HiveAppModel(store: MemoryPairingStore());
    await tester.pumpWidget(host(SessionTitle(tab: tab, model: model)));
    // 转圈一直在动，pumpAndSettle 等不到头
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.text('连接断开，正在重连…'), findsOneWidget);
    expect(find.byType(HexSpinner), findsOneWidget);
    expect(find.text('本轮完成'), findsNothing);

    model.connecting = true;
    model.notifyListeners();
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.text('正在连接电脑…'), findsOneWidget);
  });

  testWidgets('an ended session says so even while reconnecting', (
    tester,
  ) async {
    final model = HiveAppModel(store: MemoryPairingStore());
    await tester.pumpWidget(
      host(SessionTitle(tab: tab, model: model, ended: true)),
    );
    await tester.pumpAndSettle();
    expect(find.text('会话已经结束'), findsOneWidget);
    expect(find.textContaining('重连'), findsNothing);
  });
}
