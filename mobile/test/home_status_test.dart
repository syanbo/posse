import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/app_state.dart';
import 'package:hive_mobile/ui/theme.dart';
import 'package:hive_mobile/workspace/models.dart';
import 'package:hive_mobile/workspace/workspace.dart';

void main() {
  testWidgets('disconnect status lives in the computer pill, list stays put', (
    tester,
  ) async {
    final model = HiveAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    model.worktrees = parseWorktrees([
      {
        'worktreeId': 'p1::w1',
        'repo': 'hive',
        'displayName': 'main',
        'path': '/Users/x/hive',
        'isMainWorktree': true,
      },
    ]);

    Future<Offset> projectAt() async {
      await tester.pumpWidget(
        MaterialApp(
          theme: hiveTheme(Brightness.dark),
          home: WorkspaceScreen(model: model),
        ),
      );
      // 转圈一直在动，按时间推进
      await tester.pump(const Duration(milliseconds: 400));
      return tester.getTopLeft(find.text('hive').last);
    }

    // 没连上：电脑胶囊写全状态，点不了的历史、新建收起来；重连失败的报错不另外浮一条
    final before = await projectAt();
    expect(find.text('连接断开，正在重连…'), findsOneWidget);
    expect(find.byTooltip('历史'), findsNothing);
    model.error = '和电脑的连接断开了，正在重连';
    final after = await projectAt();
    expect(after, before);
    expect(find.text('和电脑的连接断开了，正在重连'), findsNothing);

    model.connecting = true;
    await projectAt();
    expect(find.text('正在连接电脑…'), findsOneWidget);
    expect(
      tester.getSize(find.text('正在连接电脑…')).width,
      greaterThanOrEqualTo(13.0 * 6),
    );
  });

  testWidgets('a disconnect by hand says so and offers to reconnect', (
    tester,
  ) async {
    final model = HiveAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    model.disconnected = true;
    await tester.pumpWidget(
      MaterialApp(
        theme: hiveTheme(Brightness.dark),
        home: WorkspaceScreen(model: model),
      ),
    );
    await tester.pump(const Duration(milliseconds: 400));
    expect(find.text('已断开'), findsOneWidget);
    expect(find.text('连接断开，正在重连…'), findsNothing);
    expect(find.textContaining('可以连接的电脑 0 台'), findsOneWidget);
  });

  // 横屏只有四百高：电脑菜单要整张能滚，不能把上面的列表压没、底部溢出
  testWidgets('the computer sheet fits a phone held sideways', (tester) async {
    tester.view.physicalSize = const Size(2622, 1206);
    tester.view.devicePixelRatio = 3;
    addTearDown(tester.view.reset);
    final model = HiveAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    await tester.pumpWidget(
      MaterialApp(
        theme: hiveTheme(Brightness.dark),
        home: WorkspaceScreen(model: model),
      ),
    );
    await tester.pump(const Duration(milliseconds: 400));
    await tester.tap(find.text('连接断开，正在重连…'));
    // 正在连时多出「断开连接」，再加一条报错：固定的几行就占满了横屏的面板高度
    model.connecting = true;
    model.error = '和电脑的连接断开了，正在重连';
    await tester.pump(const Duration(milliseconds: 400));
    await tester.pump(const Duration(milliseconds: 400));
    expect(tester.takeException(), isNull);
    expect(find.text('电脑'), findsOneWidget);
    await tester.scrollUntilVisible(
      find.text('长按一台电脑可以改名或忘掉它'),
      100,
      scrollable: find.byType(Scrollable).last,
    );
    expect(tester.takeException(), isNull);
  });
}
