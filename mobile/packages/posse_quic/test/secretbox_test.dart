import 'dart:typed_data';

import 'package:posse_quic/posse_quic.dart';
import 'package:test/test.dart';

void main() {
  final key = Uint8List.fromList(List.generate(32, (i) => i));
  final nonce = Uint8List.fromList(List.generate(24, (i) => 100 + i));

  test('seal and open round-trip through the Go library', () {
    for (final n in [0, 1, 300, 64 << 10, (1 << 20) + 3]) {
      final plain = Uint8List.fromList(List.generate(n, (i) => i * 7));
      final box = secretboxSeal(key, nonce, plain);
      expect(box.length, n + secretboxOverhead);
      expect(secretboxOpen(key, nonce, box), plain);
    }
  });

  test('leaves room in front for the frame header', () {
    final plain = Uint8List.fromList([1, 2, 3]);
    final frame = secretboxSeal(key, nonce, plain, prefix: 5);
    expect(frame.length, 5 + secretboxOverhead + 3);
    expect(frame.sublist(0, 5), everyElement(0));
    expect(frame.sublist(5), secretboxSeal(key, nonce, plain));
    expect(secretboxOpen(key, nonce, Uint8List.sublistView(frame, 5)), plain);
  });

  test('takes views without copying them first', () {
    final backing = Uint8List(64)..fillRange(0, 64, 9);
    final plain = Uint8List.sublistView(backing, 10, 20);
    final box = secretboxSeal(key, nonce, plain);
    final padded = Uint8List(box.length + 5)..setRange(5, box.length + 5, box);
    expect(secretboxOpen(key, nonce, Uint8List.sublistView(padded, 5)), plain);
  });

  test('rejects tampered boxes, a wrong key or nonce and short input', () {
    final box = secretboxSeal(key, nonce, Uint8List.fromList([1, 2, 3]));
    final tampered = Uint8List.fromList(box)..[box.length - 1] ^= 1;
    expect(secretboxOpen(key, nonce, tampered), isNull);
    final other = Uint8List.fromList(key)..[0] ^= 1;
    expect(secretboxOpen(other, nonce, box), isNull);
    final otherNonce = Uint8List.fromList(nonce)..[23] ^= 1;
    expect(secretboxOpen(key, otherNonce, box), isNull);
    expect(secretboxOpen(key, nonce, Uint8List(15)), isNull);
  });
}
