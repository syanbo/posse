import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/protocol/link.dart';
import 'package:posse_mobile/protocol/pairing.dart';
import 'package:posse_mobile/ui/line_icons.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/workspace/models.dart';
import 'package:posse_mobile/workspace/workspace.dart';

void main() {
  testWidgets('project rows show the icon set on the desktop sidebar', (
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
        'displayName': 'posse',
        'path': '/Users/x/posse',
        'isMainWorktree': true,
        'repoIcon': {'text': '🚀'},
      },
      {
        'worktreeId': 'p2::w2',
        'repo': 'chat',
        'displayName': 'chat',
        'path': '/Users/x/chat',
        'isMainWorktree': true,
        // 还没连上取不了图片：先显示文件夹
        'repoIcon': {'image': 'p2-1.png'},
      },
    ]);
    expect(model.worktrees[1].iconImage, 'p2-1.png');
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: WorkspaceScreen(model: model),
      ),
    );
    await tester.pump(const Duration(milliseconds: 400));
    expect(find.text('🚀'), findsOneWidget);
    expect(
      find.byWidgetPredicate(
        (w) => w is LineIconView && w.icon == LineIcon.folder,
      ),
      findsOneWidget,
    );
  });

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

  testWidgets(
    'workspace rows follow the desktop sidebar and the meta switches',
    (tester) async {
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
      );
      model.worktrees = parseWorktrees([
        {
          'worktreeId': 'p1::w1',
          'repo': 'posse',
          'displayName': 'posse',
          'path': '/Users/x/posse',
          'branch': 'main',
          'isMainWorktree': true,
        },
        {
          'worktreeId': 'p1::w2',
          'repo': 'posse',
          'displayName': 'login',
          'path': '/Users/x/.posse/worktrees/posse/login',
          'branch': 'feat/login',
          'isWorktree': true,
        },
        {
          'worktreeId': 'p1::w3',
          'repo': 'posse',
          'displayName': 'fix-crash',
          'path': '/Users/x/elsewhere',
          'branch': 'fix-crash',
          'isWorktree': true,
        },
      ]);
      model.link = _droppedLink();
      Future<void> pump() async {
        await tester.pumpWidget(
          MaterialApp(
            theme: posseTheme(Brightness.dark),
            home: WorkspaceScreen(model: model),
          ),
        );
        await tester.pump(const Duration(milliseconds: 400));
      }

      await pump();
      expect(find.text('主工作区'), findsOneWidget);
      expect(find.text('login'), findsOneWidget);
      // 分支和名字不同才写；加进来的 worktree 名字就是分支，不再写一遍
      expect(find.text('feat/login'), findsOneWidget);
      expect(find.text('fix-crash'), findsOneWidget);
      expect(find.text('main'), findsOneWidget);
      expect(find.text('~/posse'), findsOneWidget);
      expect(
        find.byWidgetPredicate(
          (w) => w is LineIconView && w.icon == LineIcon.branch,
        ),
        findsNWidgets(2),
      );

      model.updateSettings((s) {
        s.projectMetaHidden = true;
        s.workspaceMetaHidden = true;
      });
      await pump();
      expect(find.text('~/posse'), findsNothing);
      expect(find.text('feat/login'), findsNothing);
      expect(find.text('main'), findsNothing);
      expect(find.text('login'), findsOneWidget);
    },
  );

  testWidgets('workspace rows fold their sessions and remember it', (
    tester,
  ) async {
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    model.saved = SavedComputers(
      computers: [SavedComputer(id: 'k', code: '')],
      active: 'k',
    );
    model.worktrees = parseWorktrees([
      {
        'worktreeId': 'p1::w1',
        'repo': 'posse',
        'displayName': 'posse',
        'path': '/Users/x/posse',
        'isMainWorktree': true,
      },
      {
        'worktreeId': 'p1::w2',
        'repo': 'posse',
        'displayName': 'login',
        'path': '/Users/x/.posse/worktrees/posse/login',
        'isWorktree': true,
      },
    ]);
    model.tabs = {
      'p1::w1': const [
        SessionTab(worktreeId: 'p1::w1', title: 'shell-a', terminalId: 't1'),
      ],
      'p1::w2': const [
        SessionTab(worktreeId: 'p1::w2', title: 'shell-b', terminalId: 't2'),
      ],
    };
    Future<void> pump() async {
      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          home: WorkspaceScreen(model: model),
        ),
      );
      await tester.pumpAndSettle();
    }

    await pump();
    expect(find.text('shell-a').hitTestable(), findsOneWidget);
    await tester.tap(find.text('主工作区'));
    await pump();
    expect(model.isWorkspaceCollapsed('p1::w1'), isTrue);
    expect(find.text('shell-a').hitTestable(), findsNothing);
    expect(find.text('shell-b').hitTestable(), findsOneWidget);
    expect(
      SavedComputers.decode(model.saved.encode())
          .computers
          .single
          .collapsedWorkspaces,
      ['p1::w1'],
    );
    await tester.tap(find.text('主工作区'));
    await pump();
    expect(find.text('shell-a').hitTestable(), findsOneWidget);
  });

  test('running order sorts once, then only inserts new sessions', () {
    final ranks = {'a': 1, 'b': 5, 'c': 3, 'd': 1};
    int rank(String id) => ranks[id]!;
    var order = updateRunOrder(null, ['a', 'b', 'c', 'd'], rank);
    expect(order, ['b', 'c', 'a', 'd']);
    // 状态变了不挪
    ranks['a'] = 5;
    order = updateRunOrder(order, ['a', 'b', 'c', 'd'], rank);
    expect(order, ['b', 'c', 'a', 'd']);
    // 新会话插到同状态那一段末尾；关掉的去掉
    ranks['e'] = 3;
    order = updateRunOrder(order, ['a', 'b', 'd', 'e'], rank);
    expect(order, ['b', 'a', 'e', 'd']);
    // 排头降了档也不挡住：新的工作中会话接在同档的 e 后面，不插到 b 前面
    ranks['b'] = 0;
    ranks['f'] = 3;
    order = updateRunOrder(order, ['a', 'b', 'd', 'e', 'f'], rank);
    expect(order, ['b', 'a', 'e', 'f', 'd']);
  });

  testWidgets('running view lists agent sessions with where they live', (
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
        'displayName': 'posse',
        'path': '/Users/x/posse',
        'isMainWorktree': true,
      },
      {
        'worktreeId': 'p1::w2',
        'repo': 'posse',
        'displayName': 'login',
        'path': '/Users/x/.posse/worktrees/posse/login',
        'isWorktree': true,
      },
      {
        'worktreeId': 'p2::w1',
        'repo': 'chat',
        'displayName': 'chat',
        'path': '/Users/x/chat',
        'isMainWorktree': true,
      },
    ]);
    model.tabs = {
      'p1::w1': const [
        SessionTab(worktreeId: 'p1::w1', title: 'shell-a', terminalId: 't1'),
      ],
      'p1::w2': const [
        SessionTab(
          worktreeId: 'p1::w2',
          title: 'fix-login',
          terminalId: 't2',
          agent: 'claude',
        ),
      ],
      'p2::w1': const [
        SessionTab(
          worktreeId: 'p2::w1',
          title: 'chat-ui',
          terminalId: 't3',
          agent: 'codex',
        ),
      ],
    };
    Future<void> pump() async {
      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          home: WorkspaceScreen(model: model),
        ),
      );
      await tester.pumpAndSettle();
    }

    await pump();
    await tester.tap(find.text('运行中'));
    await pump();
    expect(model.settings.homeByStatus, isTrue);
    expect(find.text('shell-a'), findsNothing);
    expect(find.text('fix-login'), findsOneWidget);
    expect(find.text('posse · login · 等连上电脑'), findsOneWidget);
    expect(find.text('chat · 等连上电脑'), findsOneWidget);
    await tester.tap(find.text('项目'));
    await pump();
    expect(find.text('shell-a'), findsOneWidget);
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
