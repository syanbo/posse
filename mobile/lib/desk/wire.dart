import 'dart:typed_data';

import '../l10n/l10n.dart';

/// 和 internal/desk/input.go 的 EncodeInput 同一条明文。小端，版本 1。
const deskEventMove = 1;
const deskEventDown = 2;
const deskEventUp = 3;
const deskEventScroll = 4;
const deskEventKeyDown = 5;
const deskEventKeyUp = 6;
const deskEventText = 7;

/// 锁屏密码：Mac 只在锁着屏时输进锁屏的密码框再回车。只发给 ready 带 `password: true` 的 Mac。
const deskEventPassword = 8;

/// 密码最多这么多字节：一条密码事件要和别的必达事件挤进一条明文（[deskInputLimit]）。
const deskPasswordMaxBytes = 1000;

/// 密码框最多让输这么多字：每个字最多 4 字节，输满也不超过 [deskPasswordMaxBytes]。
const deskPasswordMaxChars = deskPasswordMaxBytes ~/ 4;

const deskButtonLeft = 1;
const deskButtonRight = 2;

/// 一条 input 明文的上限，和 InputPlainLimit 一致。
const deskInputLimit = 1175;

const deskKeyHid = {
  'Esc': 0x29,
  'Tab': 0x2B,
  'Enter': 0x28,
  '→': 0x4F,
  '←': 0x50,
  '↓': 0x51,
  '↑': 0x52,
};

/// 快捷键栏上切空间的三个键：都是 ⌃ 加方向键，对应系统设置里调度中心的默认快捷键
/// （向左、向右移动一个空间，调度中心）。不吃掉用户按着的修饰键。
/// 键是代号，显示的文字在 [deskSpaceLabel]。
const deskSpaceKeys = {
  'spaceLeft': '←',
  'spaceRight': '→',
  'missionControl': '↑',
};

String deskSpaceLabel(AppLocalizations l, String key) => switch (key) {
  'spaceLeft' => l.deskSpaceLeft,
  'spaceRight' => l.deskSpaceRight,
  'missionControl' => l.deskMissionControl,
  _ => key,
};

/// 整屏 0–1 换成绝对坐标 0–65535。
int deskAxis(double unit) => (unit.clamp(0.0, 1.0) * 65535).round();

/// 滚动事件里一个主屏高度对应的单位数，和 ScrollPerScreen 一致。
const deskScrollPerScreen = 4096;

/// 手指位移（右、下为正，逻辑像素）换成主屏高度的 1/4096。pictureHeight 是画面
/// 此刻在手机上的高度（含缩放），Mac 再乘上主屏的高，内容就贴着手指走。
/// CG 的正方向是上、右。纵向取反。Mac 不再翻一次。
(double dx, double dy) deskScrollUnits(
  double dx,
  double dy,
  double pictureHeight,
) {
  if (pictureHeight <= 0) return (0, 0);
  final k = deskScrollPerScreen / pictureHeight;
  return (dx * k, -dy * k);
}

/// 滚动换成整数单位，不足 1 的留着下次加上。
class DeskScrollCarry {
  double _x = 0;
  double _y = 0;

  (int, int) take(double dx, double dy) {
    _x += dx;
    _y += dy;
    final x = _x.truncate().clamp(-32768, 32767).toInt();
    final y = _y.truncate().clamp(-32768, 32767).toInt();
    _x -= x;
    _y -= y;
    return (x, y);
  }
}

class DeskWireException implements Exception {
  DeskWireException(this.message);
  final String message;
  @override
  String toString() => message;
}

class DeskWireEvent {
  DeskWireEvent.move(this.x, this.y)
    : kind = deskEventMove,
      seq = 0,
      button = 0,
      clicks = 0,
      dx = 0,
      dy = 0,
      hid = 0,
      mods = 0,
      text = '';

  DeskWireEvent.down({
    required this.button,
    required this.clicks,
    required this.x,
    required this.y,
  }) : kind = deskEventDown,
       seq = 0,
       dx = 0,
       dy = 0,
       hid = 0,
       mods = 0,
       text = '';

  DeskWireEvent.up({
    required this.button,
    required this.clicks,
    required this.x,
    required this.y,
  }) : kind = deskEventUp,
       seq = 0,
       dx = 0,
       dy = 0,
       hid = 0,
       mods = 0,
       text = '';

  DeskWireEvent.scroll({required this.dx, required this.dy})
    : kind = deskEventScroll,
      seq = 0,
      x = 0,
      y = 0,
      button = 0,
      clicks = 0,
      hid = 0,
      mods = 0,
      text = '';

  DeskWireEvent.keyDown({required this.hid, required this.mods})
    : kind = deskEventKeyDown,
      seq = 0,
      x = 0,
      y = 0,
      button = 0,
      clicks = 0,
      dx = 0,
      dy = 0,
      text = '';

  DeskWireEvent.keyUp({required this.hid, required this.mods})
    : kind = deskEventKeyUp,
      seq = 0,
      x = 0,
      y = 0,
      button = 0,
      clicks = 0,
      dx = 0,
      dy = 0,
      text = '';

  DeskWireEvent.text(this.text, this.mods)
    : kind = deskEventText,
      seq = 0,
      x = 0,
      y = 0,
      button = 0,
      clicks = 0,
      dx = 0,
      dy = 0,
      hid = 0;

  DeskWireEvent.password(this.text)
    : kind = deskEventPassword,
      seq = 0,
      x = 0,
      y = 0,
      button = 0,
      clicks = 0,
      dx = 0,
      dy = 0,
      hid = 0,
      mods = 0;

  final int kind;
  int seq;
  final int x;
  final int y;
  final int button;
  final int clicks;
  final int dx;
  final int dy;
  final int hid;
  final int mods;
  final String text;

  // 编好的字节和编的时候的序号：没确认的事件每个包都重带，不用每次重编（序号变了才重编）
  Uint8List? _wire;
  var _wireSeq = -1;
}

/// [event] 编成的字节，放不进一条明文的照样给出（调用方按长度判断）；编不了的抛 [DeskWireException]。
Uint8List _eventBytes(DeskWireEvent event) {
  final cached = event._wire;
  if (cached != null && event._wireSeq == event.seq) return cached;
  final out = BytesBuilder(copy: false);
  _writeEvent(out, event);
  final bytes = out.toBytes();
  event._wire = bytes;
  event._wireSeq = event.seq;
  return bytes;
}

Uint8List encodeDeskInput(List<DeskWireEvent> events) {
  if (events.length > 255) {
    throw DeskWireException('too many input events');
  }
  final body = BytesBuilder(copy: false);
  for (final event in events) {
    _writeEvent(body, event);
  }
  final payload = body.toBytes();
  if (2 + payload.length > deskInputLimit) {
    throw DeskWireException('input packet is too long');
  }
  final out = Uint8List(2 + payload.length);
  out[0] = 1;
  out[1] = events.length;
  out.setRange(2, out.length, payload);
  return out;
}

/// 按发生顺序切成若干条不超过上限的明文。单条放不进就丢掉这一条。
/// 每条事件只编一次、按长度往包里装：和逐个试着整批重编（放得下就加进去，放不下就先发掉手里的）切法相同、
/// 字节相同，确认迟迟不到、攒了几百条没确认的事件时不是平方级。
List<Uint8List> encodeDeskPackets(List<DeskWireEvent> events) {
  final out = <Uint8List>[];
  final batch = <Uint8List>[];
  var size = 2; // 版本号和事件数各一个字节
  void send() {
    final packet = Uint8List(size);
    packet[0] = 1;
    packet[1] = batch.length;
    var off = 2;
    for (final b in batch) {
      packet.setRange(off, off + b.length, b);
      off += b.length;
    }
    out.add(packet);
    batch.clear();
    size = 2;
  }

  for (final event in events) {
    Uint8List? bytes;
    try {
      bytes = _eventBytes(event);
    } on DeskWireException {
      bytes = null;
    }
    if (bytes != null &&
        batch.length < 255 &&
        size + bytes.length <= deskInputLimit) {
      batch.add(bytes);
      size += bytes.length;
      continue;
    }
    if (batch.isNotEmpty) send();
    // 一条就超长、或者编不了：发不出去，丢掉
    if (bytes != null && 2 + bytes.length <= deskInputLimit) {
      batch.add(bytes);
      size += bytes.length;
    }
  }
  if (batch.isNotEmpty) send();
  return out;
}

/// 解开失败时返回 null，整包作废。
List<DeskWireEvent>? decodeDeskInput(Uint8List bytes) {
  if (bytes.length < 2 || bytes[0] != 1) return null;
  final data = ByteData.sublistView(bytes);
  final n = bytes[1];
  var p = 2;
  final out = <DeskWireEvent>[];
  for (var i = 0; i < n; i++) {
    if (p >= bytes.length) return null;
    final kind = bytes[p];
    p++;
    switch (kind) {
      case deskEventMove:
        if (bytes.length - p < 4) return null;
        out.add(
          DeskWireEvent.move(
            data.getUint16(p, Endian.little),
            data.getUint16(p + 2, Endian.little),
          ),
        );
        p += 4;
      case deskEventDown:
      case deskEventUp:
        if (bytes.length - p < 10) return null;
        final event = kind == deskEventDown
            ? DeskWireEvent.down(
                button: bytes[p + 4],
                clicks: bytes[p + 5],
                x: data.getUint16(p + 6, Endian.little),
                y: data.getUint16(p + 8, Endian.little),
              )
            : DeskWireEvent.up(
                button: bytes[p + 4],
                clicks: bytes[p + 5],
                x: data.getUint16(p + 6, Endian.little),
                y: data.getUint16(p + 8, Endian.little),
              );
        event.seq = data.getUint32(p, Endian.little);
        out.add(event);
        p += 10;
      case deskEventScroll:
        if (bytes.length - p < 8) return null;
        final event = DeskWireEvent.scroll(
          dx: data.getInt16(p + 4, Endian.little),
          dy: data.getInt16(p + 6, Endian.little),
        );
        event.seq = data.getUint32(p, Endian.little);
        out.add(event);
        p += 8;
      case deskEventKeyDown:
      case deskEventKeyUp:
        if (bytes.length - p < 7) return null;
        final event = kind == deskEventKeyDown
            ? DeskWireEvent.keyDown(
                hid: data.getUint16(p + 4, Endian.little),
                mods: bytes[p + 6],
              )
            : DeskWireEvent.keyUp(
                hid: data.getUint16(p + 4, Endian.little),
                mods: bytes[p + 6],
              );
        event.seq = data.getUint32(p, Endian.little);
        out.add(event);
        p += 7;
      case deskEventText:
        if (bytes.length - p < 7) return null;
        final seq = data.getUint32(p, Endian.little);
        final mods = bytes[p + 4];
        final nb = data.getUint16(p + 5, Endian.little);
        p += 7;
        if (bytes.length - p < nb) return null;
        final text = _utf8Decode(bytes.sublist(p, p + nb));
        if (text == null) return null;
        final event = DeskWireEvent.text(text, mods);
        event.seq = seq;
        out.add(event);
        p += nb;
      case deskEventPassword:
        if (bytes.length - p < 6) return null;
        final seq = data.getUint32(p, Endian.little);
        final nb = data.getUint16(p + 4, Endian.little);
        p += 6;
        if (bytes.length - p < nb) return null;
        final text = _utf8Decode(bytes.sublist(p, p + nb));
        if (text == null) return null;
        out.add(DeskWireEvent.password(text)..seq = seq);
        p += nb;
      default:
        return null;
    }
  }
  if (p != bytes.length) return null;
  return out;
}

void _writeEvent(BytesBuilder out, DeskWireEvent event) {
  switch (event.kind) {
    case deskEventMove:
      out.addByte(deskEventMove);
      _u16(out, event.x);
      _u16(out, event.y);
    case deskEventDown:
    case deskEventUp:
      out.addByte(event.kind);
      _u32(out, event.seq);
      out.addByte(event.button & 0xff);
      out.addByte(event.clicks & 0xff);
      _u16(out, event.x);
      _u16(out, event.y);
    case deskEventScroll:
      out.addByte(deskEventScroll);
      _u32(out, event.seq);
      _i16(out, event.dx);
      _i16(out, event.dy);
    case deskEventKeyDown:
    case deskEventKeyUp:
      out.addByte(event.kind);
      _u32(out, event.seq);
      _u16(out, event.hid);
      out.addByte(event.mods & 0xff);
    case deskEventText:
      final raw = _utf8(event.text);
      if (raw == null || raw.length > 65535) {
        throw DeskWireException('bad text');
      }
      out.addByte(deskEventText);
      _u32(out, event.seq);
      out.addByte(event.mods & 0xff);
      _u16(out, raw.length);
      out.add(raw);
    case deskEventPassword:
      final raw = _utf8(event.text);
      if (raw == null || raw.length > 65535) {
        throw DeskWireException('bad text');
      }
      out.addByte(deskEventPassword);
      _u32(out, event.seq);
      _u16(out, raw.length);
      out.add(raw);
    default:
      throw DeskWireException('unknown kind');
  }
}

void _u16(BytesBuilder out, int value) {
  final n = value & 0xffff;
  out.addByte(n & 0xff);
  out.addByte((n >> 8) & 0xff);
}

void _i16(BytesBuilder out, int value) {
  _u16(out, value);
}

void _u32(BytesBuilder out, int value) {
  final n = value & 0xffffffff;
  out.addByte(n & 0xff);
  out.addByte((n >> 8) & 0xff);
  out.addByte((n >> 16) & 0xff);
  out.addByte((n >> 24) & 0xff);
}

Uint8List? _utf8(String text) {
  try {
    return _encodeUtf8(text);
  } on DeskWireException {
    return null;
  }
}

Uint8List _encodeUtf8(String text) {
  final out = BytesBuilder(copy: false);
  for (final rune in text.runes) {
    if (rune < 0x80) {
      out.addByte(rune);
    } else if (rune < 0x800) {
      out.addByte(0xc0 | (rune >> 6));
      out.addByte(0x80 | (rune & 0x3f));
    } else if (rune < 0x10000) {
      out.addByte(0xe0 | (rune >> 12));
      out.addByte(0x80 | ((rune >> 6) & 0x3f));
      out.addByte(0x80 | (rune & 0x3f));
    } else if (rune <= 0x10ffff) {
      out.addByte(0xf0 | (rune >> 18));
      out.addByte(0x80 | ((rune >> 12) & 0x3f));
      out.addByte(0x80 | ((rune >> 6) & 0x3f));
      out.addByte(0x80 | (rune & 0x3f));
    } else {
      throw DeskWireException('bad text');
    }
  }
  return out.toBytes();
}

String? _utf8Decode(Uint8List bytes) {
  final codes = <int>[];
  var i = 0;
  while (i < bytes.length) {
    final a = bytes[i];
    if (a < 0x80) {
      codes.add(a);
      i++;
      continue;
    }
    int need;
    int min;
    int value;
    if (a & 0xe0 == 0xc0) {
      need = 1;
      min = 0x80;
      value = a & 0x1f;
    } else if (a & 0xf0 == 0xe0) {
      need = 2;
      min = 0x800;
      value = a & 0x0f;
    } else if (a & 0xf8 == 0xf0) {
      need = 3;
      min = 0x10000;
      value = a & 0x07;
    } else {
      return null;
    }
    if (i + need >= bytes.length) return null;
    for (var k = 1; k <= need; k++) {
      final cont = bytes[i + k];
      if (cont & 0xc0 != 0x80) return null;
      value = (value << 6) | (cont & 0x3f);
    }
    if (value < min || value > 0x10ffff) return null;
    if (value >= 0xd800 && value <= 0xdfff) return null;
    codes.add(value);
    i += need + 1;
  }
  return String.fromCharCodes(codes);
}

/// 一次观看里的输入。必达事件序号从 1 递增。
/// 还没被确认的，之后每个包都重带。移动按调用方的刷新合并，只留最新的一笔，确认前不重发。
class DeskInputLink {
  DeskInputLink({required this.onPacket});

  final void Function(Uint8List packet) onPacket;

  var sent = 0;
  var confirmed = 0;

  final _unacked = <DeskWireEvent>[];
  final _buttons = <int, (int, int, int)>{};
  final _keys = <int, int>{};
  DeskWireEvent? _move;
  var _moveAfter = 0;
  var _moveFresh = false;
  var _next = 1;
  var _hold = 0;

  bool get needsRetry => _unacked.isNotEmpty;
  bool get hasFreshMove => _moveFresh;

  void reset() {
    _unacked.clear();
    _buttons.clear();
    _keys.clear();
    _move = null;
    _moveAfter = 0;
    _moveFresh = false;
    _next = 1;
    sent = 0;
    confirmed = 0;
    _hold = 0;
  }

  /// 确认号之前的必达事件丢掉。确认号只往前走。
  bool noteAck(int ack) {
    if (ack <= confirmed) return false;
    confirmed = ack;
    _unacked.removeWhere((event) => event.seq <= ack);
    return true;
  }

  void move(int x, int y) {
    _move = DeskWireEvent.move(x, y);
    _moveAfter = _unacked.isEmpty ? 0 : _unacked.last.seq;
    _moveFresh = true;
    for (final button in _buttons.keys.toList()) {
      final clicks = _buttons[button]!.$3;
      _buttons[button] = (x, y, clicks);
    }
  }

  void flushMove() {
    if (!_moveFresh) return;
    _emit();
  }

  void retransmit() => _emit();

  /// 一组事件进同一个包。松开和按键松开走这里，不进移动的刷新合并。
  void batch(void Function() body) {
    _hold++;
    try {
      body();
    } finally {
      _hold--;
      if (_hold == 0) _emit();
    }
  }

  void buttonDown(int button, int clicks, int x, int y) {
    _buttons[button] = (x, y, clicks);
    _reliable(DeskWireEvent.down(button: button, clicks: clicks, x: x, y: y));
  }

  void buttonUp(int button, int clicks, int x, int y) {
    _buttons.remove(button);
    _reliable(DeskWireEvent.up(button: button, clicks: clicks, x: x, y: y));
  }

  void scroll(int dx, int dy) {
    if (dx == 0 && dy == 0) return;
    _reliable(DeskWireEvent.scroll(dx: dx, dy: dy));
  }

  void keyDown(int hid, int mods) {
    _keys[hid] = mods;
    _reliable(DeskWireEvent.keyDown(hid: hid, mods: mods));
  }

  void keyUp(int hid, int mods) {
    _keys.remove(hid);
    _reliable(DeskWireEvent.keyUp(hid: hid, mods: mods));
  }

  void text(String value, int mods) {
    if (value.isEmpty) return;
    final parts = _textChunks(value, 1000);
    if (parts.length == 1) {
      _reliable(DeskWireEvent.text(parts.first, mods));
      return;
    }
    batch(() {
      for (final part in parts) {
        _reliable(DeskWireEvent.text(part, mods));
      }
    });
  }

  /// 锁屏密码，一条必达事件：Mac 当场查锁屏，没锁就不打。
  /// 空的、一条明文装不下的不发（返回 false）：装不下的占着序号却永远发不出去。
  bool password(String value) {
    final raw = _utf8(value);
    if (raw == null || raw.isEmpty || raw.length > deskPasswordMaxBytes) {
      return false;
    }
    _reliable(DeskWireEvent.password(value));
    return true;
  }

  /// 还按着的鼠标键和键盘键各补一条松开。
  void release() {
    final buttons = _buttons.entries.toList();
    final keys = _keys.entries.toList();
    if (buttons.isEmpty && keys.isEmpty) return;
    batch(() {
      for (final entry in buttons) {
        final (x, y, clicks) = entry.value;
        buttonUp(entry.key, clicks, x, y);
      }
      for (final entry in keys) {
        keyUp(entry.key, entry.value);
      }
    });
  }

  void _reliable(DeskWireEvent event) {
    event.seq = _next++;
    sent = event.seq;
    _unacked.add(event);
    if (_hold == 0) _emit();
  }

  void _emit() {
    if (_unacked.isEmpty && !_moveFresh) return;
    final events = <DeskWireEvent>[];
    var placed = !_moveFresh;
    for (final event in _unacked) {
      if (!placed && event.seq > _moveAfter) {
        events.add(_move!);
        placed = true;
      }
      events.add(event);
    }
    if (!placed && _move != null) events.add(_move!);
    _moveFresh = false;
    for (final packet in encodeDeskPackets(events)) {
      onPacket(packet);
    }
  }
}

List<String> _textChunks(String value, int maxBytes) {
  final codes = value.runes.toList();
  final out = <String>[];
  var from = 0;
  var bytes = 0;
  for (var i = 0; i < codes.length; i++) {
    final n = _runeBytes(codes[i]);
    if (bytes + n > maxBytes && i > from) {
      out.add(String.fromCharCodes(codes.sublist(from, i)));
      from = i;
      bytes = 0;
    }
    bytes += n;
  }
  if (from < codes.length) {
    out.add(String.fromCharCodes(codes.sublist(from)));
  }
  return out;
}

int _runeBytes(int rune) {
  if (rune < 0x80) return 1;
  if (rune < 0x800) return 2;
  if (rune < 0x10000) return 3;
  return 4;
}

/// 退格键的 HID 码，系统键盘删字时发它。
const deskBackspaceHid = 0x2A;

/// 隐藏输入框里垫着的几个零宽空格：框里有字，系统键盘按退格才有反应。
const deskTypingPad = '​​​​';

/// 隐藏输入框从 [before] 变成 [after]：按字符（不拆代理对）比出共同的开头，后面删掉了几个字（每个是一次退格）、
/// 新打了什么。不在每次打字后清空输入框：打得快时系统输入法还没收到清空，下一次又把前面的字带回来，
/// Mac 上就重复打出前缀。
(int deleted, String added) deskTypedDiff(String before, String after) {
  final a = before.runes.toList();
  final b = after.runes.toList();
  var same = 0;
  while (same < a.length && same < b.length && a[same] == b[same]) {
    same++;
  }
  return (a.length - same, String.fromCharCodes(b.sublist(same)));
}
