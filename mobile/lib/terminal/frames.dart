import 'dart:typed_data';

const termFrameMagic = 0x74;
const termFrameVersion = 1;
const termOpOutput = 1;
const termOpSnapshotStart = 2;
const termOpSnapshotChunk = 3;
const termOpSnapshotEnd = 4;

class TermFrame {
  const TermFrame({
    required this.op,
    required this.sid,
    required this.seq,
    required this.payload,
  });

  final int op;
  final int sid;
  final int seq;
  final Uint8List payload;
}

TermFrame? parseTermFrame(Uint8List bytes) {
  if (bytes.length < 16) return null;
  if (bytes[0] != termFrameMagic || bytes[1] != termFrameVersion) return null;
  final data = ByteData.sublistView(bytes);
  final hi = data.getUint32(8, Endian.little);
  final lo = data.getUint32(12, Endian.little);
  return TermFrame(
    op: bytes[2],
    sid: data.getUint32(4, Endian.little),
    seq: (hi << 32) | lo,
    payload: Uint8List.sublistView(bytes, 16),
  );
}

class TermBytes {
  const TermBytes.output(this.bytes) : snapshot = false;
  const TermBytes.snapshot(this.bytes) : snapshot = true;

  final Uint8List bytes;
  final bool snapshot;
}

/// 把 Orca 的 16 字节头帧收成「要写进终端的一段字节」。
/// 快照按 start/chunk/end 拼好再一次性写入，输出帧原样交出。
class TermAssembler {
  final BytesBuilder _chunks = BytesBuilder(copy: false);
  var _open = false;

  TermBytes? add(Uint8List raw) {
    final frame = parseTermFrame(raw);
    if (frame == null) return null;
    switch (frame.op) {
      case termOpSnapshotStart:
        _chunks.clear();
        _open = true;
        return null;
      case termOpSnapshotChunk:
        if (_open) _chunks.add(frame.payload);
        return null;
      case termOpSnapshotEnd:
        if (!_open) return null;
        _open = false;
        final bytes = _chunks.toBytes();
        _chunks.clear();
        return TermBytes.snapshot(bytes);
      case termOpOutput:
        if (_open || frame.payload.isEmpty) return null;
        return TermBytes.output(Uint8List.fromList(frame.payload));
      default:
        return null;
    }
  }
}

class TermKey {
  const TermKey(this.label, this.bytes);
  final String label;
  final List<int> bytes;
}

/// 软键盘上方的专用键。方向键是 VT 的 A/B/C/D，不带回车。
const termKeys = <TermKey>[
  TermKey('Esc', [0x1b]),
  TermKey('Tab', [0x09]),
  TermKey('⇧Tab', [0x1b, 0x5b, 0x5a]),
  TermKey('^C', [0x03]),
  TermKey('↑', [0x1b, 0x5b, 0x41]),
  TermKey('↓', [0x1b, 0x5b, 0x42]),
  TermKey('←', [0x1b, 0x5b, 0x44]),
  TermKey('→', [0x1b, 0x5b, 0x43]),
];
