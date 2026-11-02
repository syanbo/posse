import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/ui/widgets.dart';
import 'package:posse_mobile/workspace/models.dart';
import 'package:posse_mobile/workspace/workspace.dart';

Map<String, Object?> _tab(String id, {String? state, String? posse}) => {
  'type': 'terminal',
  'terminal': id,
  'title': id,
  'launchAgent': 'claude',
  'agentStatus': ?state == null ? null : {'state': state},
  'posseStatus': ?posse,
};

void main() {
  test('posseStatus wins for display, state still drives logic', () {
    final tabs = parseTabs('p::w', [
      _tab('a', state: 'working', posse: 'tasks'),
      _tab('b', state: 'done', posse: 'unread'),
      _tab('c', state: 'working', posse: 'starting'),
      _tab('d', posse: 'lost'),
      // 旧常驻进程不给 posseStatus：照 state
      _tab('e', state: 'working'),
    ]);
    expect(tabs.map((t) => t.shown), [
      AgentState.tasks,
      AgentState.unread,
      AgentState.starting,
      AgentState.lost,
      AgentState.working,
    ]);
    // 等任务时 state 照旧是工作中，在不在干活的判断不变
    expect(agentStateOf(tabs.first.state), AgentState.working);
    final zh = lookupAppLocalizations(const Locale('zh'));
    expect(tabs.map((t) => sessionDetail(zh, t)), [
      '等待任务',
      '本轮完成，还没看',
      '启动中',
      '未恢复，点开时拉起',
      '工作中',
    ]);
  });

  test('worktrees with only lost terminals still get their tabs fetched', () {
    final rows = parseWorktrees([
      {'worktreeId': 'p::a', 'liveTerminalCount': 0, 'posseTerminalCount': 2},
      // 旧常驻进程不给 posseTerminalCount：照在跑的个数
      {'worktreeId': 'p::b', 'liveTerminalCount': 1},
    ]);
    expect(rows.map((r) => (r.live, r.terminals)), [(0, 2), (1, 1)]);
  });

  testWidgets('new states paint, animated and still', (tester) async {
    for (final still in [false, true]) {
      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          home: MediaQuery(
            data: MediaQueryData(disableAnimations: still),
            child: const Row(
              children: [
                StatusHex(state: AgentState.starting),
                StatusHex(state: AgentState.tasks),
                StatusHex(state: AgentState.unread),
                StatusHex(state: AgentState.lost),
              ],
            ),
          ),
        ),
      );
      await tester.pump(const Duration(milliseconds: 500));
      expect(tester.takeException(), isNull);
      expect(find.byType(StatusHex), findsNWidgets(4));
    }
    // 离开后计时器停掉，测试不会报还有挂着的计时器
    await tester.pumpWidget(const SizedBox());
  });
}
