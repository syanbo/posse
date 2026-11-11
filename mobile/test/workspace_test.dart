import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/workspace/models.dart';

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

}
