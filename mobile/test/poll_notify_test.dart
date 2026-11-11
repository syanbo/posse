import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/protocol/rpc.dart';

/// worktree.ps 和 session.tabs.list 照 [rows]、[tabs] 回；[fail] 为真时 worktree.ps 报错。
class _Rpc implements PhoneRpc {
  List<Map<String, Object?>> rows = [];
  Map<String, List<Map<String, Object?>>> tabs = {};
  bool fail = false;

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    if (method == 'worktree.ps') {
      if (fail) throw StateError('offline');
      return {'worktrees': jsonDecode(jsonEncode(rows))};
    }
    if (method == 'session.tabs.list') {
      return {'tabs': jsonDecode(jsonEncode(tabs[params['worktree']] ?? []))};
    }
    return const {};
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class _Model extends PosseAppModel {
  _Model(this.fake)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());
  final _Rpc fake;
  @override
  PhoneRpc? get rpc => fake;
}

Map<String, Object?> _row(String id) => {
  'worktreeId': id,
  'repo': 'posse',
  'displayName': 'name $id',
  'path': '/x/$id',
  'branch': 'main',
  'liveTerminalCount': 2,
  'posseTerminalCount': 2,
  'isMainWorktree': false,
  'isWorktree': true,
  'lastActivityAt': 100,
  'repoIcon': {'text': 'P'},
};

Map<String, Object?> _tab(String terminal) => {
  'type': 'terminal',
  'terminal': terminal,
  'title': 'tab $terminal',
  'launchAgent': 'claude',
  'posseStatus': 'idle',
  'agentStatus': {
    'state': 'idle',
    'agentType': 'claude',
    'toolName': 'Bash',
    'interactivePrompt': '',
    'providerSession': {'id': 's-$terminal', 'transcriptPath': '/t/$terminal'},
  },
};

/// 模型里现在显示的东西（工作区和标签的每个字段、报错、过期标记），按顺序写成一段文字，比较前后是否一样。
String _shown(PosseAppModel m) => jsonEncode({
  'rows': [
    for (final r in m.worktrees)
      [
        r.id,
        r.repo,
        r.name,
        r.path,
        r.branch,
        r.live,
        r.terminals,
        r.isMain,
        r.isWorktree,
        r.lastActivityAt,
        r.iconText,
        r.iconImage,
      ],
  ],
  'tabs': {
    for (final e in m.tabs.entries)
      e.key: [
        for (final t in e.value)
          [
            t.worktreeId,
            t.title,
            t.terminalId,
            t.agent,
            t.state,
            t.sessionId,
            t.transcriptPath,
            t.toolName,
            t.prompt,
            t.posse,
          ],
      ],
  },
  'error': '${m.error}',
  'stale': m.tabsStale,
});

void main() {
  late _Rpc rpc;
  late _Model model;
  var notified = 0;

  void reset() {
    rpc = _Rpc()
      ..rows = [_row('w1'), _row('w2')]
      ..tabs = {
        'w1': [_tab('t1'), _tab('t2')],
        'w2': [_tab('t3')],
      };
    model = _Model(rpc);
    notified = 0;
    model.addListener(() => notified++);
  }

  setUp(reset);

  // 定时轮询拉回来的和上次一样：不通知，模型里的数据照样是这一轮的。
  test('an unchanged poll does not notify', () async {
    await model.refreshWorktrees(poll: true);
    expect(notified, 1);
    final before = _shown(model);
    await model.refreshWorktrees(poll: true);
    expect(notified, 1);
    expect(_shown(model), before);
    // 不是定时轮询的刷新（回到前台、操作之后）照旧通知
    await model.refreshWorktrees();
    expect(notified, 2);
  });

  // 工作区、标签的任何一个字段，顺序，增删：变了就通知，模型里是新的。
  test('every change in the polled data notifies', () async {
    final changes = <String, void Function()>{
      for (final key in [
        'repo',
        'displayName',
        'path',
        'branch',
        'lastActivityAt',
      ])
        'row $key': () =>
            rpc.rows[0][key] = key == 'lastActivityAt' ? 101 : 'changed',
      'row liveTerminalCount': () => rpc.rows[0]['liveTerminalCount'] = 3,
      'row posseTerminalCount': () => rpc.rows[0]['posseTerminalCount'] = 3,
      'row isMainWorktree': () => rpc.rows[0]['isMainWorktree'] = true,
      'row isWorktree': () => rpc.rows[0]['isWorktree'] = false,
      'row icon text': () => rpc.rows[0]['repoIcon'] = {'text': 'Q'},
      'row icon image': () => rpc.rows[0]['repoIcon'] = {'image': 'a.png'},
      'row order': () => rpc.rows = rpc.rows.reversed.toList(),
      'row added': () => rpc.rows.add(_row('w3')),
      'row removed': () => rpc.rows.removeLast(),
      'row id': () => rpc.rows[0]['worktreeId'] = 'w9',
      'tab title': () => rpc.tabs['w1']![0]['title'] = 'renamed',
      'tab terminal': () => rpc.tabs['w1']![0]['terminal'] = 't9',
      'tab agent': () => rpc.tabs['w1']![0]['launchAgent'] = 'codex',
      'tab posse status': () => rpc.tabs['w1']![0]['posseStatus'] = 'waiting',
      for (final key in ['state', 'toolName', 'interactivePrompt'])
        'tab $key': () =>
            (rpc.tabs['w1']![0]['agentStatus'] as Map)[key] = 'changed',
      'tab session id': () =>
          ((rpc.tabs['w1']![0]['agentStatus'] as Map)['providerSession']
                  as Map)['id'] =
              'other',
      'tab transcript': () =>
          ((rpc.tabs['w1']![0]['agentStatus'] as Map)['providerSession']
                  as Map)['transcriptPath'] =
              '/other',
      'tab order': () => rpc.tabs['w1'] = rpc.tabs['w1']!.reversed.toList(),
      'tab added': () => rpc.tabs['w2']!.add(_tab('t4')),
      'tab removed': () => rpc.tabs['w1']!.removeLast(),
      'worktree without terminals': () => rpc.rows[1]['posseTerminalCount'] = 0,
    };
    for (final MapEntry(key: name, value: change) in changes.entries) {
      reset();
      await model.refreshWorktrees(poll: true);
      final before = _shown(model);
      final count = notified;
      change();
      await model.refreshWorktrees(poll: true);
      expect(notified, count + 1, reason: name);
      expect(_shown(model), isNot(before), reason: name);
      // 再拉一次，没再变：不通知
      await model.refreshWorktrees(poll: true);
      expect(notified, count + 1, reason: '$name, polled again');
    }
  });

  // 报错过、标签标成过期之后：下一轮拉到同样的数据也通知（报错要消掉、标签不再是过期的）。
  test('a poll after an error or stale tabs notifies', () async {
    await model.refreshWorktrees(poll: true);
    rpc.fail = true;
    await model.refreshWorktrees(poll: true);
    expect(model.error, isNotNull);
    final count = notified;
    rpc.fail = false;
    await model.refreshWorktrees(poll: true);
    expect(model.error, isNull);
    expect(notified, count + 1);

    model.tabsStale = true;
    await model.refreshWorktrees(poll: true);
    expect(model.tabsStale, isFalse);
    expect(notified, count + 2);
  });
}
