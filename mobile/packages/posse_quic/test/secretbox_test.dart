import 'dart:typed_data';

import 'package:posse_quic/posse_quic.dart';
import 'package:test/test.dart';

void main() {
  final key = Uint8List.fromList(List.generate(32, (i) => i));
  final nonce = Uint8List.fromList(List.generate(24, (i) => 100 + i));

  test('seal and open round-trip through the Go library', () {
    for (final n in [0, 1, 300, 64 << 10, (1 << 20) + 3]) {
      final plain = Uint8List.fromList(List.generate(n, (i) => i * 7));
      final frame = secretboxSeal(key, nonce, plain);
      expect(frame.length, n + secretboxNonceLength + secretboxOverhead);
      expect(frame.sublist(0, 24), nonce);
      expect(secretboxOpen(key, frame), plain);
    }
  });

  test('takes views without copying them first', () {
    final backing = Uint8List(64)..fillRange(0, 64, 9);
    final plain = Uint8List.sublistView(backing, 10, 20);
    final frame = secretboxSeal(key, nonce, plain);
    final padded = Uint8List(frame.length + 5)..setRange(5, frame.length + 5, frame);
    expect(secretboxOpen(key, Uint8List.sublistView(padded, 5)), plain);
  });

  test('rejects tampered frames, a wrong key and short input', () {
    final frame = secretboxSeal(key, nonce, Uint8List.fromList([1, 2, 3]));
    final tampered = Uint8List.fromList(frame)..[frame.length - 1] ^= 1;
    expect(secretboxOpen(key, tampered), isNull);
    final other = Uint8List.fromList(key)..[0] ^= 1;
    expect(secretboxOpen(other, frame), isNull);
    expect(secretboxOpen(key, Uint8List(39)), isNull);
  });
}
