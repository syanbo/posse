import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/chat/echo.dart';
import 'package:posse_mobile/chat/models.dart';

ChatMessage user(String id, DateTime at) => ChatMessage(
  id: id,
  role: 'user',
  blocks: const [ChatBlock(type: 'text', text: 'x')],
  timestamp: at.millisecondsSinceEpoch,
  source: 'transcript',
);

void main() {
  final now = DateTime(2026, 10, 18, 21);

  test('新出现的用户消息按顺序认领最早的回显', () {
    final echoes = LocalEchoes();
    final old = user('u0', now.subtract(const Duration(hours: 1)));
    expect(echoes.reconcile([old], now: now), isFalse);
    final first = echoes.add('一', now: now);
    echoes.add('二', now: now);
    // 已经见过的旧消息不认领
    expect(echoes.reconcile([old], now: now), isFalse);
    expect(echoes.items, hasLength(2));
    expect(
      echoes.reconcile([
        old,
        user('u1', now.add(const Duration(seconds: 2))),
      ], now: now),
      isTrue,
    );
    expect(echoes.items.single.text, '二');
    expect(echoes.items.contains(first), isFalse);
  });

  test('往上翻出来的更早消息不认领，电脑时钟慢一点也认', () {
    final echoes = LocalEchoes()..add('一', now: now);
    echoes.reconcile([
      user('older', now.subtract(const Duration(minutes: 10))),
    ], now: now);
    expect(echoes.items, hasLength(1));
    echoes.reconcile([
      user('skewed', now.subtract(const Duration(seconds: 5))),
    ], now: now);
    expect(echoes.isEmpty, isTrue);
  });

  test('一直等不到的回显过一阵撤掉', () {
    final echoes = LocalEchoes()..add('一', now: now);
    expect(
      echoes.reconcile(const [], now: now.add(const Duration(minutes: 6))),
      isTrue,
    );
    expect(echoes.isEmpty, isTrue);
  });
}
