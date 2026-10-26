import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/history/history.dart';
import 'package:posse_mobile/history/models.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/workspace/models.dart';

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

    await tester.tap(find.text('按文件夹'));
    await tester.pumpAndSettle();
    expect(model.settings.historyByFolder, isTrue);
    expect(find.text('posse'), findsOneWidget);
    // 组标题加上那一行的目录名
    expect(find.text('scratch'), findsNWidgets(2));
  });
}
