import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/terminal/frames.dart';

Uint8List frame(int op, List<int> payload, {int sid = 7, int seq = 3}) {
  final head = ByteData(16);
  head.setUint8(0, termFrameMagic);
  head.setUint8(1, termFrameVersion);
  head.setUint8(2, op);
  head.setUint32(4, sid, Endian.little);
  head.setUint32(8, (seq >> 32) & 0xffffffff, Endian.little);
  head.setUint32(12, seq & 0xffffffff, Endian.little);
  return Uint8List.fromList([...head.buffer.asUint8List(), ...payload]);
}

void main() {
  test('parses the 16-byte header and reassembles a snapshot', () {
    final parsed = parseTermFrame(
      frame(termOpOutput, [0x61], seq: (1 << 32) + 5),
    );
    expect(parsed, isNotNull);
    expect(parsed!.op, termOpOutput);
    expect(parsed.sid, 7);
    expect(parsed.seq, (1 << 32) + 5);
    expect(parsed.payload, [0x61]);
    expect(parseTermFrame(Uint8List(8)), isNull);

    final asm = TermAssembler();
    expect(asm.add(frame(termOpSnapshotStart, [0x7b, 0x7d])), isNull);
    expect(asm.add(frame(termOpSnapshotChunk, [0x68, 0x69])), isNull);
    final snap = asm.add(frame(termOpSnapshotEnd, []));
    expect(snap!.snapshot, isTrue);
    expect(snap.bytes, [0x68, 0x69]);
    final out = asm.add(frame(termOpOutput, [0x1b, 0x5b, 0x41]));
    expect(out!.snapshot, isFalse);
    expect(out.bytes, [0x1b, 0x5b, 0x41]);
  });

  test('key bar bytes are the terminal sequences', () {
    final byLabel = {for (final key in termKeys) key.label: key.bytes};
    expect(byLabel['Esc'], [0x1b]);
    expect(byLabel['Tab'], [0x09]);
    expect(byLabel['⇧Tab'], [0x1b, 0x5b, 0x5a]);
    expect(byLabel['^C'], [0x03]);
    expect(byLabel['↑'], [0x1b, 0x5b, 0x41]);
    expect(byLabel['↓'], [0x1b, 0x5b, 0x42]);
    expect(byLabel['←'], [0x1b, 0x5b, 0x44]);
    expect(byLabel['→'], [0x1b, 0x5b, 0x43]);
  });
}
