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
///
/// 换尺寸时新旧两个订阅会短暂同时在推，只收 [sid] 那一路。还不知道新订阅的编号时先攒着，
/// 知道以后（[accept]）再按顺序放出来，第一份快照不会丢。
class TermAssembler {
  final BytesBuilder _chunks = BytesBuilder(copy: false);
  var _open = false;
  int? _sid;
  var _waiting = false; // 换了订阅、还不知道新编号
  final _pending = <TermFrame>[];

  /// 换订阅时调用：之后的帧先攒着，等 [accept] 告诉收哪一路。
  void expect() {
    _sid = null;
    _waiting = true;
    _pending.clear();
    _chunks.clear();
    _open = false;
  }

  /// 只收编号为 [sid] 的帧，返回之前攒下的这一路的内容。
  List<TermBytes> accept(int sid) {
    _sid = sid;
    _waiting = false;
    final out = <TermBytes>[];
    for (final frame in _pending) {
      if (frame.sid != sid) continue;
      final piece = _frame(frame);
      if (piece != null) out.add(piece);
    }
    _pending.clear();
    return out;
  }

  TermBytes? add(Uint8List raw) {
    final frame = parseTermFrame(raw);
    if (frame == null) return null;
    if (_waiting) {
      _pending.add(frame);
      return null;
    }
    if (_sid != null && frame.sid != _sid) return null;
    return _frame(frame);
  }

  TermBytes? _frame(TermFrame frame) {
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
