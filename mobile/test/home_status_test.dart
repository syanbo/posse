import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/protocol/link.dart';
import 'package:posse_mobile/protocol/pairing.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/workspace/models.dart';
import 'package:posse_mobile/workspace/workspace.dart';

void main() {
  testWidgets('disconnect status lives in the computer pill, list stays put', (
    tester,
  ) async {
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    model.worktrees = parseWorktrees([
      {
        'worktreeId': 'p1::w1',
        'repo': 'posse',
        'displayName': 'main',
        'path': '/Users/x/posse',
        'isMainWorktree': true,
      },
    ]);
    // 连上过、断线后后台在重连：有 link，没有 rpc
    model.link = _droppedLink();

    Future<Offset> projectAt() async {
      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          home: WorkspaceScreen(model: model),
        ),
      );
      // 转圈一直在动，按时间推进
      await tester.pump(const Duration(milliseconds: 400));
      return tester.getTopLeft(find.text('posse').last);
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

  testWidgets('a connect that failed and stopped says so, not reconnecting', (
    tester,
  ) async {
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    model.saved = SavedComputers(
      computers: [SavedComputer(id: 'k', code: '', name: '公司')],
      active: 'k',
    );
    model.error = 'QuicException: context deadline exceeded';
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: WorkspaceScreen(model: model),
      ),
    );
    await tester.pump(const Duration(milliseconds: 400));
    expect(model.failed, isTrue);
    expect(find.text('公司 · 连不上'), findsOneWidget);
    expect(find.text('连接断开，正在重连…'), findsNothing);
    expect(find.text('重试'), findsOneWidget);
  });

  // 竖屏电脑多了：只有中间的列表滚，标题和底下的按钮不动
  testWidgets('the computer sheet keeps its buttons with many computers', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(1206, 2622);
    tester.view.devicePixelRatio = 3;
    addTearDown(tester.view.reset);
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    model.saved = SavedComputers(
      computers: [
        for (var i = 0; i < 8; i++)
          SavedComputer(id: 'k$i', code: '', name: '电脑$i'),
      ],
      active: 'k0',
    );
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: WorkspaceScreen(model: model),
      ),
    );
    await tester.pump(const Duration(milliseconds: 400));
    await tester.tap(find.text('电脑0 · 连不上'));
    await tester.pump(const Duration(milliseconds: 400));
    await tester.pump(const Duration(milliseconds: 400));
    expect(tester.takeException(), isNull);
    final screen = tester.view.physicalSize.height / 3;
    final add = tester.getRect(find.text('扫码添加电脑'));
    final hint = tester.getRect(find.text('长按一台电脑可以改名或忘掉它'));
    expect(hint.bottom, lessThanOrEqualTo(screen));
    final first = tester.getTopLeft(find.text('电脑0'));
    await tester.drag(find.text('电脑3'), const Offset(0, -300));
    await tester.pump(const Duration(milliseconds: 400));
    expect(tester.getRect(find.text('扫码添加电脑')), add);
    // 列表滚了，按钮不动
    expect(tester.getTopLeft(find.text('电脑0')).dy, lessThan(first.dy));
    expect(tester.getRect(find.text('电脑7')).bottom, lessThan(add.top));
  });

  testWidgets('a disconnect by hand says so and offers to reconnect', (
    tester,
  ) async {
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    model.disconnected = true;
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
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
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: WorkspaceScreen(model: model),
      ),
    );
    await tester.pump(const Duration(milliseconds: 400));
    await tester.tap(find.text('连不上'));
    // 正在连时多出「断开连接」，再加一条报错：固定的几行就占满了横屏的面板高度
    model.connecting = true;
    model.error = '和电脑的连接断开了，正在重连';
    await tester.pump(const Duration(milliseconds: 400));
    await tester.pump(const Duration(milliseconds: 400));
    expect(tester.takeException(), isNull);
    expect(find.text('断开连接'), findsOneWidget);
    await tester.scrollUntilVisible(
      find.text('长按一台电脑可以改名或忘掉它'),
      100,
      scrollable: find.byType(Scrollable).last,
    );
    expect(tester.takeException(), isNull);
  });
}

/// 连上过又断了的连接：后台在重连，[PosseLink.rpc] 为空。不会真的去连。
PosseLink _droppedLink() => PosseLink(
  offer: const PairingOffer(
    endpoint: 'ws://a:1',
    endpoints: ['ws://a:1'],
    deviceToken: 't',
    publicKeyB64: 'k',
  ),
  connect: (url, cancel) => throw StateError('offline'),
);
