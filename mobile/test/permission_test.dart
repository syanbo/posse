import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/chat/permission.dart';
import 'package:posse_mobile/chat/prompt.dart';

void main() {
  test('stale tab state keeps a permission that just arrived', () {
    var live = const LivePermission().receive(
      requestId: 'r',
      tool: 'Bash',
      input: {'command': 'go test'},
      alreadyWaiting: false,
    );
    live = live.observe(waiting: false);
    expect(live.requestId, 'r');
    expect(permissionSummary(live.input), 'go test');
    live = live.observe(waiting: true);
    live = live.settle();
    expect(live.requestId, isNull);
    expect(live.settled, isTrue);
    live = live.observe(waiting: false);
    expect(live.settled, isFalse);
  });

  test('summary stops at 200 runes', () {
    final summary = permissionSummary({'file_path': '啊' * 230});
    expect(summary.runes.length, 200);
    expect(summary.contains('…'), isFalse);
    expect(permissionBehavior(allowKey), 'allow');
    expect(permissionBehavior(denyKey), 'deny');
    expect(permissionBehavior('\r'), isNull);
  });
}
