import 'dart:convert';
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

  test('快照元数据里的列数和行数一起带上', () {
    final asm = TermAssembler();
    expect(
      asm.add(
        frame(termOpSnapshotStart, utf8.encode('{"cols":120,"rows":40}')),
      ),
      isNull,
    );
    expect(asm.add(frame(termOpSnapshotChunk, utf8.encode('hi'))), isNull);
    final piece = asm.add(frame(termOpSnapshotEnd, const []));
    expect(piece!.snapshot, isTrue);
    expect(piece.cols, 120);
    expect(piece.rows, 40);
    expect(utf8.decode(piece.bytes), 'hi');
  });

  test('key bar bytes are the terminal sequences', () {
    final byLabel = {for (final key in termKeys) key.label: key.bytes};
    expect(byLabel['Esc'], [0x1b]);
    expect(byLabel['Tab'], [0x09]);
    expect(byLabel['Enter'], [0x0d]);
    // Enter 排在 Tab 后面
    final labels = termKeys.map((k) => k.label).toList();
    expect(labels.indexOf('Enter'), labels.indexOf('Tab') + 1);
    // Claude 排队消息立即发送（chat:sendNow 的 Ctrl+X Ctrl+S），紧跟在 Enter 后面
    expect(byLabel['立即发送'], [0x18, 0x13]);
    expect(labels.indexOf('立即发送'), labels.indexOf('Enter') + 1);
    expect(byLabel['⇧Tab'], [0x1b, 0x5b, 0x5a]);
    expect(byLabel['^C'], [0x03]);
    expect(byLabel['↑'], [0x1b, 0x5b, 0x41]);
    expect(byLabel['↓'], [0x1b, 0x5b, 0x42]);
    expect(byLabel['←'], [0x1b, 0x5b, 0x44]);
    expect(byLabel['→'], [0x1b, 0x5b, 0x43]);
  });

  test(
    'the bullet Claude prints is drawn as a text dot, even split across chunks',
    () {
      final fix = BulletFix();
      final bullet = utf8.encode('⏺ 好');
      expect(utf8.decode(fix.apply(Uint8List.fromList(bullet))), '● 好');
      // ⏺ 被切在两块之间：前半截留到下一块
      final a = fix.apply(Uint8List.fromList([0x41, 0xe2, 0x8f]));
      final b = fix.apply(Uint8List.fromList([0xba, 0x42]));
      expect(utf8.decode([...a, ...b]), 'A●B');
      // 别的以 E2 开头的字符原样
      expect(
        utf8.decode(fix.apply(Uint8List.fromList(utf8.encode('→ ✳')))),
        '→ ✳',
      );
    },
  );
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

  test('a size flterm shrank through resubscribes even when it comes back the same', () {
    final gate = ResizeGate();
    expect(gate.report(49, 44), isTrue);
    expect(gate.report(49, 44), isFalse);
    // 页面推入、键盘动画途中一瞬间很小：不订阅，但本地内容已被裁
    expect(gate.report(49, 3), isFalse);
    expect(gate.report(12, 0), isFalse);
    expect(gate.report(49, 44), isTrue);
    expect(gate.report(49, 44), isFalse);
    expect(gate.report(49, 22), isTrue);
    expect((gate.cols, gate.rows), (49, 22));
  });
}
