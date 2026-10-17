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
  _overlapTest();
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

Uint8List _frame(int op, int sid, List<int> payload) {
  final b = Uint8List(16 + payload.length);
  b[0] = termFrameMagic;
  b[1] = termFrameVersion;
  b[2] = op;
  ByteData.sublistView(b).setUint32(4, sid, Endian.little);
  b.setRange(16, b.length, payload);
  return b;
}

void _overlapTest() {
  test(
    'a new subscription only takes its own frames, buffered until known',
    () {
      final asm = TermAssembler()..expect();
      // 旧订阅还在推，新订阅的快照在知道编号之前就到了
      expect(asm.add(_frame(termOpOutput, 1, [0x61])), isNull);
      expect(asm.add(_frame(termOpSnapshotStart, 2, [])), isNull);
      expect(asm.add(_frame(termOpSnapshotChunk, 2, [0x62])), isNull);
      expect(asm.add(_frame(termOpSnapshotEnd, 2, [])), isNull);
      final early = asm.accept(2);
      expect(early.single.snapshot, isTrue);
      expect(early.single.bytes, [0x62]);
      expect(asm.add(_frame(termOpOutput, 1, [0x61])), isNull);
      expect(asm.add(_frame(termOpOutput, 2, [0x63]))!.bytes, [0x63]);
    },
  );
}
