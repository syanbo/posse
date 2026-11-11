import 'dart:math';
import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/desk/wire.dart';

/// 原来的切包写法：每加一条就把整批重编一遍试试，放不下先发掉手里的，单条放不下的丢掉。拿来逐字节对照。
List<Uint8List> _oldPackets(List<DeskWireEvent> events) {
  final out = <Uint8List>[];
  var batch = <DeskWireEvent>[];
  for (final event in events) {
    Uint8List? next;
    try {
      next = encodeDeskInput([...batch, event]);
    } on DeskWireException {
      next = null;
    }
    if (next != null) {
      batch.add(event);
      continue;
    }
    if (batch.isNotEmpty) {
      out.add(encodeDeskInput(batch));
      batch = [];
    }
    try {
      encodeDeskInput([event]);
      batch = [event];
    } on DeskWireException {
      // 一条就超长，发不出去。
    }
  }
  if (batch.isNotEmpty) out.add(encodeDeskInput(batch));
  return out;
}

DeskWireEvent _random(Random r, int seq) {
  String text(int n) => String.fromCharCodes([
    for (var i = 0; i < n; i++)
      switch (r.nextInt(4)) {
        0 => 0x4e00 + r.nextInt(500), // 三字节
        1 => 0x1f600 + r.nextInt(40), // 四字节
        _ => 0x61 + r.nextInt(26),
      },
  ]);
  final DeskWireEvent e = switch (r.nextInt(9)) {
    0 => DeskWireEvent.move(r.nextInt(4000), r.nextInt(3000)),
    1 => DeskWireEvent.down(button: 1, clicks: 2, x: 5, y: 6),
    2 => DeskWireEvent.up(button: 2, clicks: 1, x: 5, y: 6),
    3 => DeskWireEvent.scroll(dx: r.nextInt(200) - 100, dy: -3),
    4 => DeskWireEvent.keyDown(hid: r.nextInt(200), mods: r.nextInt(16)),
    5 => DeskWireEvent.keyUp(hid: r.nextInt(200), mods: 0),
    6 => DeskWireEvent.password(text(r.nextInt(300))),
    7 => DeskWireEvent.text(
      // 有时一条就放不进一包；有时带一个单独的代理项（编不了）
      r.nextInt(10) == 0
          ? '${text(3)}\ud800'
          : text(r.nextInt(r.nextBool() ? 40 : 700)),
      r.nextInt(4),
    ),
    _ => DeskWireEvent.text('x', 0),
  };
  if (e.kind != deskEventMove) e.seq = seq;
  return e;
}

void expectSamePackets(List<Uint8List> got, List<Uint8List> want, String why) {
  expect(got.length, want.length, reason: why);
  for (var i = 0; i < want.length; i++) {
    expect(got[i], want[i], reason: '$why packet $i');
  }
}

void main() {
  // 各种事件、长短不一的文字（一条放不下的、编不了的）、事件数超过 255：切法和每个字节都和原来一样。
  test('packets match the old packing byte for byte', () {
    final r = Random(11);
    for (var round = 0; round < 300; round++) {
      final n = round % 10 == 0 ? 600 : r.nextInt(80);
      final events = [for (var i = 0; i < n; i++) _random(r, i + 1)];
      expectSamePackets(
        encodeDeskPackets(events),
        _oldPackets(events),
        'round $round',
      );
      // 同一批再编一次（走缓存）也一样
      expectSamePackets(
        encodeDeskPackets(events),
        _oldPackets(events),
        'round $round again',
      );
    }
    // 小事件很多：最短的必达事件（松键）8 字节，一包按长度上限装 146 个就满了，255 个的上限碰不到
    final many = [
      for (var i = 0; i < 700; i++)
        DeskWireEvent.keyUp(hid: i % 50, mods: 0)..seq = i + 1,
    ];
    expectSamePackets(encodeDeskPackets(many), _oldPackets(many), 'many');
    expect(encodeDeskPackets(many).first[1], (deskInputLimit - 2) ~/ 8);
  });

  // 编过之后序号变了：重编，不用旧的字节。
  test('a cached event is re-encoded when its sequence changes', () {
    final e = DeskWireEvent.keyDown(hid: 4, mods: 1)..seq = 7;
    encodeDeskPackets([e]);
    e.seq = 9;
    expect(encodeDeskPackets([e]).single, encodeDeskInput([e]));
  });

  // 确认停住、攒了很多没确认的事件，中途确认一部分、再接着打字：每个包都和原来的写法编出来的一样。
  test('a stalled input link sends the same bytes as before', () {
    final r = Random(5);
    final packets = <Uint8List>[];
    final link = DeskInputLink(onPacket: packets.add);
    for (var i = 0; i < 400; i++) {
      packets.clear();
      switch (r.nextInt(6)) {
        case 0:
          link.move(r.nextInt(1000), r.nextInt(800));
          link.flushMove();
        case 1:
          link.batch(() {
            link.buttonDown(1, 1, 3, 4);
            link.buttonUp(1, 1, 3, 4);
          });
        case 2:
          link.text(
            String.fromCharCodes([
              for (var k = 0; k < r.nextInt(60) + 1; k++) 0x61 + k % 26,
            ]),
            0,
          );
        case 3:
          link.keyDown(4, 0);
        case 4:
          if (i % 50 == 0) link.noteAck(link.sent - r.nextInt(30));
          link.retransmit();
        default:
          link.scroll(0, 2);
      }
      // 这一步发出的包里的事件按顺序拼起来，交给原来的写法切：包的切法和字节都要一样
      final events = [for (final p in packets) ...decodeDeskInput(p)!];
      expectSamePackets(packets, _oldPackets(events), 'step $i');
    }
  });
}
