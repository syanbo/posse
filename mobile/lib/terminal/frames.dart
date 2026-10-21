import 'dart:convert';
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
  const TermBytes.output(this.bytes) : snapshot = false, cols = 0, rows = 0;
  const TermBytes.snapshot(this.bytes, {this.cols = 0, this.rows = 0})
    : snapshot = true;

  final Uint8List bytes;
  final bool snapshot;

  /// 快照时终端的列数、行数（快照开头的元数据里带着），电脑布局按它排；不知道时为 0。
  final int cols;
  final int rows;
}

/// 把 Orca 的 16 字节头帧收成「要写进终端的一段字节」。
/// 快照按 start/chunk/end 拼好再一次性写入，输出帧原样交出。
///
/// 换尺寸时新旧两个订阅会短暂同时在推，只收 [sid] 那一路。还不知道新订阅的编号时先攒着，
/// 知道以后（[accept]）再按顺序放出来，第一份快照不会丢。
class TermAssembler {
  final BytesBuilder _chunks = BytesBuilder(copy: false);
  var _open = false;
  var _cols = 0;
  var _rows = 0;
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
        _cols = 0;
        _rows = 0;
        try {
          final meta = jsonDecode(utf8.decode(frame.payload));
          if (meta is Map) {
            if (meta['cols'] is num) _cols = (meta['cols'] as num).toInt();
            if (meta['rows'] is num) _rows = (meta['rows'] as num).toInt();
          }
        } catch (_) {}
        return null;
      case termOpSnapshotChunk:
        if (_open) _chunks.add(frame.payload);
        return null;
      case termOpSnapshotEnd:
        if (!_open) return null;
        _open = false;
        final bytes = _chunks.toBytes();
        _chunks.clear();
        return TermBytes.snapshot(bytes, cols: _cols, rows: _rows);
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
  TermKey('Enter', [0x0d]),
  // Claude Code 的 chat:sendNow：干活时排着队的消息马上交给模型，不等这一轮做完。
  // 默认绑定还有 Ctrl+Enter，但那要 kitty 键盘协议，手机终端发不出来，用 Ctrl+X Ctrl+S
  TermKey('立即发送', [0x18, 0x13]),
  TermKey('⇧Tab', [0x1b, 0x5b, 0x5a]),
  TermKey('^C', [0x03]),
  TermKey('↑', [0x1b, 0x5b, 0x41]),
  TermKey('↓', [0x1b, 0x5b, 0x42]),
  TermKey('←', [0x1b, 0x5b, 0x44]),
  TermKey('→', [0x1b, 0x5b, 0x43]),
];

/// Claude Code 每条回复前的圆点是 ⏺（U+23FA）。手机的等宽字体里没有它，iOS 退到 Apple Color Emoji，
/// 画成一个蓝底的按钮图标；换成同宽的 ●（U+25CF）。两个都是三字节 UTF-8，长度不变。
/// 输出是分块来的，一个字符可能被切在两块之间，切开的前半截留到下一块再接上。
class BulletFix {
  final _carry = <int>[];

  static const _from = [0xe2, 0x8f, 0xba];
  static const _to = [0xe2, 0x97, 0x8f];

  Uint8List apply(Uint8List chunk) {
    final input = _carry.isEmpty
        ? chunk
        : Uint8List.fromList([..._carry, ...chunk]);
    _carry.clear();
    var end = input.length;
    // 末尾是 ⏺ 的前一两个字节时先留着
    if (end >= 1 && input[end - 1] == _from[0]) {
      end -= 1;
    } else if (end >= 2 &&
        input[end - 2] == _from[0] &&
        input[end - 1] == _from[1]) {
      end -= 2;
    }
    _carry.addAll(input.sublist(end));
    Uint8List? out;
    for (var i = 0; i + 2 < end; i++) {
      if (input[i] == _from[0] &&
          input[i + 1] == _from[1] &&
          input[i + 2] == _from[2]) {
        out ??= Uint8List.fromList(input.sublist(0, end));
        out.setRange(i, i + 3, _to);
        i += 2;
      }
    }
    return out ??
        (end == input.length ? input : Uint8List.sublistView(input, 0, end));
  }
}

/// flterm 在每次布局后都按算出来的格子数改本地终端，哪怕只是页面推入、键盘弹起途中的一瞬间很小的尺寸。
/// 备用屏幕（Claude Code 的全屏界面）缩小时被裁掉的格子回不来，agent 又只重画变了的格子，
/// 所以本地尺寸只要变过，回到能用的尺寸时就得重新订阅拿一份快照，哪怕和上次订阅的一样大。
class ResizeGate {
  int cols = 0;
  int rows = 0;
  var _dirty = false;

  /// 记下 flterm 报上来的尺寸，返回要不要按 [cols]、[rows] 重新订阅。
  bool report(int c, int r) {
    if (c == cols && r == rows && !_dirty) return false;
    // 太小的尺寸不订阅（常驻进程会按它排版），但本地内容已经被裁过了
    if (c < 20 || r < 8) {
      _dirty = true;
      return false;
    }
    cols = c;
    rows = r;
    _dirty = false;
    return true;
  }

  /// 电脑布局时本地终端按电脑的尺寸排，手机这块的列数另算（[set]），不走 [report]。
  void set(int c, int r) {
    cols = c;
    rows = r;
  }
}
