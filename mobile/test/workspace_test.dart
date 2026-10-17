import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/workspace/models.dart';

void main() {
  test('tab frames replace the list and end keeps the caller list', () {
    final tabs = tabsFromFrame('p::w', {
      'type': 'snapshot',
      'tabs': [
        {
          'type': 'terminal',
          'terminal': 't1',
          'title': 'claude',
          'launchAgent': 'claude',
          'agentStatus': {
            'state': 'waiting',
            'toolName': 'Bash',
            'interactivePrompt':
                '{"approval":{"tool":"Bash","summary":"go test"}}',
            'providerSession': {'id': 's1', 'transcriptPath': '/tmp/s1.jsonl'},
          },
        },
        {
          'type': 'terminal',
          'terminal': 't2',
          'title': 'cursor',
          'launchAgent': 'cursor',
        },
      ],
    });
    expect(tabs, isNotNull);
    expect(tabs!.first.eligible, isTrue);
    expect(tabs.first.state, 'waiting');
    expect(tabs.first.sessionId, 's1');
    expect(tabs.last.eligible, isFalse);
    expect(tabsFromFrame('p::w', {'type': 'end'}), isNull);
    expect(tabsFromFrame('p::w', {'type': 'updated'}), isNull);
  });

  test('worktree rows keep the phone status names', () {
    final rows = parseWorktrees([
      {
        'worktreeId': 'p::w',
        'repo': 'hive',
        'displayName': 'main',
        'path': '/src/hive',
        'branch': 'main',
        'status': 'permission',
        'liveTerminalCount': 1,
      },
    ]);
    expect(rows.single.subtitle, 'main');
    expect(statusLabel(rows.single.status), '待处理');
  });
}
