import 'dart:io';
import 'dart:typed_data';
import 'dart:ui';

import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/desk/direct.dart';
import 'package:posse_mobile/desk/keys.dart';
import 'package:posse_mobile/desk/mods.dart';
import 'package:posse_mobile/desk/status.dart';
import 'package:posse_mobile/desk/wire.dart';
import 'package:posse_mobile/l10n/l10n.dart';

final zh = lookupAppLocalizations(const Locale('zh'));

void main() {
  test('input bytes match EncodeInput', () {
    final events = [
      DeskWireEvent.move(100, 200),
      DeskWireEvent.down(button: 1, clicks: 2, x: 100, y: 200)..seq = 1,
      DeskWireEvent.up(button: 2, clicks: 1, x: 1, y: 2)..seq = 2,
      DeskWireEvent.scroll(dx: -4, dy: 30)..seq = 3,
      DeskWireEvent.keyDown(hid: 0x04, mods: 0x0A)..seq = 4,
      DeskWireEvent.keyUp(hid: 0xE3, mods: 0x08)..seq = 5,
      DeskWireEvent.text('你好a', 0)..seq = 6,
      DeskWireEvent.text('c', 0x08)..seq = 7,
    ];
    final raw = encodeDeskInput(events);
    expect(
      _hex(raw),
      '0108016400c800020100000001026400c8000302000000020101000200'
      '0403000000fcff1e00050400000004000a0605000000e300080706000000'
      '000700e4bda0e5a5bd61070700000008010063',
    );
    final back = decodeDeskInput(raw)!;
    expect(back, hasLength(8));
    expect(back[0].kind, deskEventMove);
    expect(back[0].x, 100);
    expect(back[0].y, 200);
    expect(back[1].seq, 1);
    expect(back[1].button, deskButtonLeft);
    expect(back[1].clicks, 2);
    expect(back[3].dx, -4);
    expect(back[3].dy, 30);
    expect(back[4].hid, 0x04);
    expect(back[4].mods, 0x0A);
    expect(back[5].kind, deskEventKeyUp);
    expect(back[6].text, '你好a');
    expect(back[6].mods, 0);
    expect(back[7].text, 'c');
    expect(back[7].mods, 0x08);
    expect(_hex(encodeDeskInput(<DeskWireEvent>[])), '0100');
    expect(decodeDeskInput(encodeDeskInput(<DeskWireEvent>[])), isEmpty);
  });

  test('password event bytes match the Mac', () {
    final raw = encodeDeskInput([DeskWireEvent.password('pw!')..seq = 9]);
    // 和 internal/desk/password_test.go 的同一组字节
    expect(_hex(raw), '010108090000000300707721');
    final back = decodeDeskInput(raw)!;
    expect(back.single.kind, deskEventPassword);
    expect(back.single.seq, 9);
    expect(back.single.text, 'pw!');
    expect(decodeDeskInput(raw.sublist(0, raw.length - 1)), isNull);
    expect(decodeDeskInput(Uint8List.fromList([...raw, 0])), isNull);
    expect(decodeDeskInput(Uint8List.fromList([1, 1, 8, 1, 0, 0])), isNull);
    expect(
      decodeDeskInput(Uint8List.fromList([1, 1, 8, 1, 0, 0, 0, 1, 0, 0xff])),
      isNull,
    );
  });

  test('password is resent until acked, then dropped', () {
    final packets = <Uint8List>[];
    final link = DeskInputLink(onPacket: packets.add);
    expect(link.password('secret'), isTrue);
    expect(link.password(''), isFalse);
    // 装不下一条明文的不占序号，后面的输入照常
    expect(link.password('x' * (deskPasswordMaxBytes + 1)), isFalse);
    expect(link.sent, 1);
    expect(packets, hasLength(1));
    link.retransmit();
    expect(packets, hasLength(2));
    final again = decodeDeskInput(packets.last)!;
    expect(again.single.kind, deskEventPassword);
    expect(again.single.seq, 1);
    link.noteAck(1);
    expect(link.needsRetry, isFalse);
  });

  test('the password sheet cannot overflow one password event', () {
    expect(deskPasswordMaxChars * 4, lessThanOrEqualTo(deskPasswordMaxBytes));
  });

  test('bad input packets are rejected whole', () {
    expect(decodeDeskInput(Uint8List.fromList([2, 0])), isNull);
    expect(decodeDeskInput(Uint8List.fromList([1, 1])), isNull);
    expect(decodeDeskInput(Uint8List.fromList([1, 1, 9])), isNull);
    expect(decodeDeskInput(Uint8List.fromList([1, 0, 0])), isNull);
    expect(
      decodeDeskInput(Uint8List.fromList([1, 1, 7, 1, 0, 0, 0, 0, 1, 0, 0xff])),
      isNull,
    );
    final move = encodeDeskInput([DeskWireEvent.move(1, 2)]);
    expect(decodeDeskInput(Uint8List.fromList([...move, 0])), isNull);
    expect(
      () => encodeDeskInput([
        for (var i = 0; i < 256; i++) DeskWireEvent.move(0, 0),
      ]),
      throwsA(isA<DeskWireException>()),
    );
    expect(
      () => encodeDeskInput([DeskWireEvent.text('a' * 65536, 0)]),
      throwsA(isA<DeskWireException>()),
    );
  });

  test('moves can be dropped, ups cannot, and ack drops the prefix', () {
    final packets = <Uint8List>[];
    final link = DeskInputLink(onPacket: packets.add);
    link.move(1, 2);
    expect(packets, isEmpty);
    link.flushMove();
    expect(packets, hasLength(1));
    expect(decodeDeskInput(packets.single)!.single.kind, deskEventMove);
    link.flushMove();
    expect(packets, hasLength(1));

    packets.clear();
    link.buttonDown(deskButtonLeft, 1, 10, 20);
    expect(link.sent, 1);
    for (var i = 0; i < 5; i++) {
      link.move(i, i);
    }
    expect(packets, hasLength(1));
    link.buttonUp(deskButtonLeft, 1, 30, 40);
    expect(packets, hasLength(2));
    final click = decodeDeskInput(packets.last)!;
    expect(click.map((event) => event.kind).toList(), [
      deskEventDown,
      deskEventMove,
      deskEventUp,
    ]);
    expect(click.last.seq, 2);
    expect(click[1].x, 4);

    expect(link.noteAck(1), isTrue);
    expect(link.noteAck(1), isFalse);
    expect(link.confirmed, 1);
    packets.clear();
    link.retransmit();
    final again = decodeDeskInput(packets.single)!;
    expect(again, hasLength(1));
    expect(again.single.kind, deskEventUp);
    expect(again.single.seq, 2);

    packets.clear();
    link.batch(() {
      link.keyDown(0x29, 0);
      link.keyUp(0x29, 0);
    });
    expect(packets, hasLength(1));
    final keys = decodeDeskInput(packets.single)!;
    expect(keys.map((event) => event.kind).toList(), [
      deskEventUp,
      deskEventKeyDown,
      deskEventKeyUp,
    ]);
    expect(keys[1].hid, 0x29);
    expect(keys[2].seq, keys[1].seq + 1);

    packets.clear();
    link.buttonDown(deskButtonRight, 1, 1, 1);
    link.release();
    final ups = decodeDeskInput(packets.last)!;
    expect(ups.last.kind, deskEventUp);
    expect(ups.last.button, deskButtonRight);
  });

  test('a click with locked modifiers presses them around the button', () {
    final packets = <Uint8List>[];
    final link = DeskInputLink(onPacket: packets.add);
    final mods = {DeskMod.command, DeskMod.shift};
    final bits = deskModBits(mods);
    expect(bits, 0x0A);
    link.batch(() {
      for (final mod in deskModPressOrder) {
        if (!mods.contains(mod)) continue;
        link.keyDown(deskModHid(mod), bits);
      }
      link.buttonDown(deskButtonLeft, 1, 1, 2);
      link.buttonUp(deskButtonLeft, 1, 1, 2);
      for (final mod in deskModPressOrder.reversed) {
        if (!mods.contains(mod)) continue;
        link.keyUp(deskModHid(mod), bits);
      }
    });
    final events = decodeDeskInput(packets.single)!;
    expect(events.map((event) => event.kind).toList(), [
      deskEventKeyDown,
      deskEventKeyDown,
      deskEventDown,
      deskEventUp,
      deskEventKeyUp,
      deskEventKeyUp,
    ]);
    expect(events[0].hid, deskModHid(DeskMod.shift));
    expect(events[1].hid, deskModHid(DeskMod.command));
    expect(events[4].hid, deskModHid(DeskMod.command));
    expect(events[5].hid, deskModHid(DeskMod.shift));
    expect(
      events.every(
        (event) =>
            event.mods == 0x0A ||
            event.kind == deskEventDown ||
            event.kind == deskEventUp,
      ),
      isTrue,
    );
  });

  test('a move before a scroll rides in the same packet, ahead of it', () {
    final packets = <Uint8List>[];
    final link = DeskInputLink(onPacket: packets.add);
    link.move(100, 200);
    link.scroll(0, -30);
    final events = decodeDeskInput(packets.single)!;
    expect(events.map((event) => event.kind).toList(), [
      deskEventMove,
      deskEventScroll,
    ]);
    expect(events.first.x, 100);
  });

  test('scroll follows the finger', () {
    // 手指划过整张画面的高，Mac 滚一整屏
    expect(deskScrollUnits(0, 200, 200), (0, -4096.0));
    expect(deskScrollUnits(100, 0, 400), (1024.0, 0));
    expect(deskScrollUnits(5, 5, 0), (0, 0));
    // 不足 1 的留到下一次
    final carry = DeskScrollCarry();
    expect(carry.take(0.6, -0.6), (0, 0));
    expect(carry.take(0.6, -0.6), (1, -1));
    expect(deskAxis(0), 0);
    expect(deskAxis(1), 65535);
  });

  test('desk keys are the HKDF of recvKey then sendKey', () {
    final keys = deriveDeskKeys(
      recvKey: Uint8List.fromList([for (var i = 0; i < 32; i++) i]),
      sendKey: Uint8List.fromList([for (var i = 0; i < 32; i++) 32 + i]),
      nonce: Uint8List.fromList([for (var i = 0; i < 16; i++) 0xA0 + i]),
    );
    expect(
      _hex(Uint8List.fromList([...keys.toPhone, ...keys.toMac])),
      '8f04928271ef6e22b39f3a0528d25ebbfd86bd414efe1241624c2c3e3a6fadee'
      '7075d509bf5b1cabc9f7fbf8a4ae00fc5bc2875b076f791e8cbfa820a9dc5cdb',
    );
    expect(keys.toPhone, hasLength(32));
    expect(keys.toMac, hasLength(32));
  });

  test('only a direct host may subscribe', () async {
    expect(deskAddressDirect(InternetAddress('127.0.0.1')), isTrue);
    // 原生收包只认 IPv4：IPv6 的地址（回环、Tailscale、链路本地）都不算直连
    expect(deskAddressDirect(InternetAddress('::1')), isFalse);
    expect(deskAddressDirect(InternetAddress('10.1.2.3')), isTrue);
    expect(deskAddressDirect(InternetAddress('192.168.50.188')), isTrue);
    expect(deskAddressDirect(InternetAddress('172.16.0.1')), isTrue);
    expect(deskAddressDirect(InternetAddress('172.31.255.255')), isTrue);
    expect(deskAddressDirect(InternetAddress('172.32.0.1')), isFalse);
    expect(deskAddressDirect(InternetAddress('169.254.1.1')), isTrue);
    expect(deskAddressDirect(InternetAddress('100.64.0.1')), isTrue);
    expect(deskAddressDirect(InternetAddress('100.127.255.255')), isTrue);
    expect(deskAddressDirect(InternetAddress('100.128.0.1')), isFalse);
    expect(deskAddressDirect(InternetAddress('8.8.8.8')), isFalse);
    expect(deskAddressDirect(InternetAddress('1.1.1.1')), isFalse);
    expect(deskAddressDirect(InternetAddress('fd7a:115c:a1e0::1')), isFalse);
    expect(deskAddressDirect(InternetAddress('fe80::1')), isFalse);
    expect(deskAddressDirect(InternetAddress('2001:db8::1')), isFalse);

    Future<List<InternetAddress>> lan(String host) async => [
      InternetAddress('192.168.1.9'),
    ];
    Future<List<InternetAddress>> pub(String host) async => [
      InternetAddress('1.1.1.1'),
    ];
    Future<List<InternetAddress>> fail(String host) async =>
        throw const SocketException('no');

    expect(await deskHostDirect('mac.local', lookup: lan), isTrue);
    expect(await deskHostDirect('tunnel.example', lookup: pub), isFalse);
    expect(await deskHostDirect('nope.example', lookup: fail), isFalse);
    expect(await deskHostDirect(''), isFalse);
    expect(
      await deskHostDirect(
        '127.0.0.1',
        lookup: (_) async => throw StateError('parsed addresses skip lookup'),
      ),
      isTrue,
    );
  });

  test(
    'an IPv6 pairing sends the picture to an IPv4 address of the same Mac',
    () async {
      Future<List<InternetAddress>> lookup(String host) async => switch (host) {
        'mac.local' => [
          InternetAddress('fd7a:115c:a1e0::5'),
          InternetAddress('192.168.50.187'),
        ],
        'v6only.local' => [InternetAddress('fd7a:115c:a1e0::5')],
        _ => throw const SocketException('no'),
      };
      // 按 IPv6 配对：换到同一台电脑的 IPv4 局域网地址，不把 IPv6 交给原生层
      expect(
        await deskUdpHost('fd7a:115c:a1e0::5', ['192.168.50.187']),
        '192.168.50.187',
      );
      // 名字同时解析出 IPv6 和 IPv4：看 IPv4 那个
      expect(await deskHostDirect('mac.local', lookup: lookup), isTrue);
      // 只有 IPv6：没有能直连的，走中转
      expect(
        await deskUdpHost('fd7a:115c:a1e0::5', [
          'v6only.local',
        ], lookup: lookup),
        isNull,
      );
    },
  );

  test('a tunnel sends the picture to a LAN address of the same Mac', () async {
    Future<List<InternetAddress>> lookup(String host) async => switch (host) {
      'tunnel.example' => [InternetAddress('1.1.1.1')],
      'mac.local' => [InternetAddress('192.168.50.187')],
      _ => throw const SocketException('no'),
    };
    // 本身能直连就用它
    expect(
      await deskUdpHost('192.168.1.2', ['10.0.0.1'], lookup: lookup),
      '192.168.1.2',
    );
    // 隧道：挑同一台电脑的第一个能直连的地址，回环跳过
    expect(
      await deskUdpHost('tunnel.example', [
        'tunnel.example',
        '127.0.0.1',
        'localhost',
        'mac.local',
        '192.168.50.187',
      ], lookup: lookup),
      'mac.local',
    );
    // 没有能直连的
    expect(
      await deskUdpHost('tunnel.example', ['127.0.0.1'], lookup: lookup),
      isNull,
    );
  });

  test('status copy is one sentence per reason', () {
    expect(deskFailText(zh, 'unsupported'), '这台电脑的 Posse 需要更新');
    expect(deskFailText(zh, 'method_not_found'), '这台电脑的 Posse 需要更新');
    expect(deskFailText(zh, 'unknown_method'), '这台电脑的 Posse 需要更新');
    expect(deskFailText(zh, 'busy'), '另一台手机正在看');
    expect(deskFailText(zh, 'locked'), 'Mac 已锁屏');
    expect(deskFailText(zh, 'permission_screen'), '请到 Mac 上允许屏幕录制');
    expect(deskFailText(zh, 'unsupported_codec'), '这台电脑不支持手机的视频编码');
    expect(deskFailText(zh, 'capture_failed'), '采集失败');
    expect(deskFailText(zh, 'other'), '连接失败');
    expect(deskEndText(zh, 'locked'), 'Mac 已锁屏');
    expect(deskEndText(zh, 'stopped'), '已在 Mac 上停止');
    expect(deskEndText(zh, 'unsubscribed'), '已停止观看');
    expect(deskEndText(zh, 'no_report'), '电脑没有收到画面报告');
    expect(deskEndText(zh, 'capture_failed'), '采集失败');
    expect(deskEndText(zh, 'other'), '连接已结束');
    expect(zh.deskNoAccessibility, '能看不能操作，请到 Mac 上打开辅助功能');
    expect(zh.deskIndirect, contains('Tailscale'));
  });

  // 隐藏输入框不再每次清空：比前后文本，只发新打的；删掉的每个字是一次退格
  test('typed text is diffed, not cleared', () {
    const pad = deskTypingPad;
    expect(deskTypedDiff(pad, '${pad}V'), (0, 'V'));
    // 打得快时系统一次交来好几个字
    expect(deskTypedDiff('${pad}V', '${pad}VQ4ok'), (0, 'Q4ok'));
    expect(deskTypedDiff('${pad}VQ4ok', '${pad}VQ4o'), (1, ''));
    // 删进垫着的零宽空格也是退格
    expect(deskTypedDiff(pad, pad.substring(1)), (1, ''));
    // 智能标点把 -- 换成 —：删两个再打一个
    expect(deskTypedDiff('${pad}a--', '${pad}a—'), (2, '—'));
    // 表情是代理对，不拆开
    expect(deskTypedDiff('$pad😀', '$pad😀😀'), (0, '😀'));
    expect(deskTypedDiff('$pad😀', pad), (1, ''));
  });
}

String _hex(Uint8List bytes) =>
    [for (final byte in bytes) byte.toRadixString(16).padLeft(2, '0')].join();
