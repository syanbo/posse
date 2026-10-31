import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/history/history.dart';
import 'package:posse_mobile/history/models.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/workspace/models.dart';
import 'package:posse_mobile/workspace/workspace.dart';

class _Rpc implements PhoneRpc {
  final asked = <(String, Map<String, Object?>)>[];

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    asked.add((method, params));
    final agent = params['agent'] as String? ?? '';
    return {
      'sessions': [
        for (final (id, ag, cwd, day) in _rows)
          if (agent.isEmpty || agent == ag)
            {
              'agent': ag,
              'id': id,
              'title': id,
              'cwd': cwd,
              'updatedAt': DateTime(2026, 10, day).toUtc().toIso8601String(),
            },
      ],
    };
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

const _rows = [
  ('修登录', 'claude', '/Users/x/posse/mobile', 26),
  ('换图标', 'codex', '/Users/x/.posse/worktrees/posse/icon', 25),
  ('读日志', 'codex', '/tmp/scratch', 24),
];

class _Model extends PosseAppModel {
  _Model(this.fake)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());

  final _Rpc fake;

  @override
  PhoneRpc? get rpc => fake;
}

void main() {
  test('folder groups take the longest project match, else the dir name', () {
    HistorySession at(String id, String cwd, int day) => HistorySession(
      agent: 'claude',
      id: id,
      title: id,
      cwd: cwd,
      branch: '',
      updatedAt: DateTime(2026, 10, day),
      messages: 0,
      path: '',
    );
    final groups = groupHistoryByFolder(
      [
        at('a', '/Users/x/posse-old/tool', 26),
        at('b', '/Users/x/posse/mobile', 25),
        at('c', '/Users/x/posse/third_party/mygo', 27),
        at('d', '', 20),
        at('e', '/Users/x/.posse/worktrees/posse/icon', 24),
      ],
      [
        ('/Users/x/posse', 'posse'),
        ('/Users/x/posse/third_party/mygo', 'mygo'),
        ('/Users/x/.posse/worktrees/posse/icon', 'posse'),
      ],
    );
    // 组按其中最近的会话排；/Users/x/posse-old 不算 posse 里面
    expect(groups.map((g) => g.$1), ['mygo', 'tool', 'posse', '未知文件夹']);
    expect(groups[2].$2.map((s) => s.id), ['b', 'e']);
  });

  test('folder grouping choice round-trips', () {
    expect(AppSettings().historyByFolder, isFalse);
    final s = AppSettings()..historyByFolder = true;
    expect(AppSettings.decode(jsonEncode(s.toJson())).historyByFolder, isTrue);
  });

  testWidgets('agent filter asks the computer, grouping switches to folders', (
    tester,
  ) async {
    final rpc = _Rpc();
    final model = _Model(rpc);
    model.worktrees = parseWorktrees([
      {
        'worktreeId': 'p::main',
        'repo': 'posse',
        'displayName': 'main',
        'path': '/Users/x/posse',
        'isMainWorktree': true,
      },
      {
        'worktreeId': 'p::icon',
        'repo': 'posse',
        'displayName': 'icon',
        'path': '/Users/x/.posse/worktrees/posse/icon',
      },
    ]);
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: HistoryScreen(model: model),
      ),
    );
    await tester.pumpAndSettle();
    expect(rpc.asked.last.$1, 'history.list');
    expect(rpc.asked.last.$2['agent'], '');
    expect(find.text('修登录'), findsOneWidget);

    await tester.tap(find.text('Codex'));
    await tester.pumpAndSettle();
    expect(rpc.asked.last.$2['agent'], 'codex');
    expect(find.text('修登录'), findsNothing);
    expect(find.text('换图标'), findsOneWidget);

    await tester.tap(find.byTooltip('按时间分组，点按文件夹'));
    await tester.pumpAndSettle();
    expect(model.settings.historyByFolder, isTrue);
    expect(find.text('posse'), findsOneWidget);
    // 只展开第一组：scratch 组收着，只有组名
    expect(find.text('scratch'), findsOneWidget);
    await tester.tap(find.text('scratch'));
    await tester.pumpAndSettle();
    // 组标题加上那一行的目录名
    expect(find.text('scratch'), findsNWidgets(2));
    await tester.tap(find.text('scratch').first);
    await tester.pumpAndSettle();
    expect(find.text('scratch'), findsOneWidget);
  });

  // 写不下时标签行横着滑，不溢出也不截
  testWidgets('agent chips scroll sideways on a narrow screen', (tester) async {
    tester.view.physicalSize = const Size(300 * 3, 700 * 3);
    tester.view.devicePixelRatio = 3;
    addTearDown(tester.view.reset);
    final rpc = _Rpc();
    final model = _Model(rpc);
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: HistoryScreen(model: model),
      ),
    );
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);
    final grok = find.text('Grok');
    expect(tester.getRect(grok).right, greaterThan(300));
    await tester.drag(find.text('Codex'), const Offset(-1000, 0));
    await tester.pumpAndSettle();
    expect(tester.getRect(grok).right, lessThanOrEqualTo(300));
    await tester.tap(grok);
    await tester.pumpAndSettle();
    expect(rpc.asked.last.$2['agent'], 'grok');
  });

  testWidgets('project page history tab only lists its own directory', (
    tester,
  ) async {
    final rpc = _Rpc();
    final model = _Model(rpc);
    model.worktrees = parseWorktrees([
      {
        'worktreeId': 'p::main',
        'repo': 'posse',
        'displayName': 'main',
        'path': '/Users/x/posse',
        'isMainWorktree': true,
      },
    ]);
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: TabScreen(model: model, worktree: model.worktrees.first, page: 2),
      ),
    );
    await tester.pumpAndSettle();
    expect(rpc.asked.last.$1, 'history.list');
    expect(rpc.asked.last.$2['cwd'], '/Users/x/posse');
    // 没有分组按钮，搜索也带上目录
    expect(find.byTooltip('按时间分组，点按文件夹'), findsNothing);
    await tester.enterText(find.byType(TextField), '登录');
    await tester.pump(const Duration(milliseconds: 400));
    await tester.pumpAndSettle();
    expect(rpc.asked.last.$1, 'history.search');
    expect(rpc.asked.last.$2['cwd'], '/Users/x/posse');
  });
}
