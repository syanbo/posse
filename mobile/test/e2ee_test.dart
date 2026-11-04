import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/protocol/e2ee.dart';

void main() {
  test('transcript vector matches the Go fixture', () {
    final context = HelloContext(
      protocol: protocolName,
      initiator: 'mobile',
      responder: 'desktop',
      transport: 'relay',
      relayHostId: 'AbCdEf0123_-xyZ9',
    );
    // 固定向量来自 Orca 的 fixture，握手记录里写的是它的分帧 2；帧本身按分帧 3 封
    final hello = Hello(
      clientPublicKey: repeated(1, 32),
      clientNonce: repeated(2, 32),
      context: context,
      framing: 2,
    );
    final ready = Ready(
      desktopPublicKey: repeated(3, 32),
      clientNonce: repeated(2, 32),
      desktopNonce: repeated(4, 32),
      context: context,
      framing: 2,
    );
    final transcript = encodeTranscript(hello, ready);
    expect(transcript.length, 1347);
    final session = deriveSession(
      shared: repeated(5, 32),
      transcript: transcript,
      clientNonce: repeated(2, 32),
      desktopNonce: repeated(4, 32),
    );
    expect(
      hexOf(session.transcriptHash),
      'ca6385f8bbf64a223fdd59587bfb67e2373891ce9e6d85ab41df8b7a20a168e3',
    );
    expect(
      hexOf(session.recvKey),
      'df17ff534df77fd3a30999f4e6200c8fcedefbb15d369301ca62c3cdfea9559a',
    );
    expect(
      hexOf(session.sendKey),
      '71365fcf8212a6d63caf909ee28de3c8f689682ef298a374136055e0ab1cde4a',
    );
    expect(
      hexOf(session.id),
      '339ae1f2bdff63481857d2813c2f19dd1f5aa4824705d5e5daeb25dae7b9196e',
    );

    final frame = session.seal(
      Uint8List.fromList(utf8.encode('hi')),
      direction: dirDesktopToMobile,
      kind: kindText,
    );
    expect(hexOf(frame), '00869982af5c7f1b336995806e50addaf9e497');
    final phone = session.asPhone();
    // 主机已经把发送计数加到 1。手机是另一套计数，从 0 解开这一帧。
    expect(
      utf8.decode(
        phone.open(frame, direction: dirDesktopToMobile, kind: kindText),
      ),
      'hi',
    );
    expect(
      () => phone.open(frame, direction: dirDesktopToMobile, kind: kindText),
      throwsA(isA<E2eeException>()),
    );
  });

  test('phone and host round-trip text and binary', () {
    final hostKeys = generateKeyPair();
    final clientKeys = generateKeyPair();
    final clientNonce = repeated(7, 32);
    final desktopNonce = repeated(8, 32);
    const context = HelloContext(
      protocol: protocolName,
      initiator: 'mobile',
      responder: 'desktop',
      transport: 'direct',
    );
    final hello = Hello(
      clientPublicKey: clientKeys.publicKey,
      clientNonce: clientNonce,
      context: context,
    );
    final ready = Ready(
      desktopPublicKey: hostKeys.publicKey,
      clientNonce: clientNonce,
      desktopNonce: desktopNonce,
      context: context,
    );
    final transcript = encodeTranscript(hello, ready);
    final host = deriveSession(
      shared: beforeNm(clientKeys.publicKey, hostKeys.secretKey),
      transcript: transcript,
      clientNonce: clientNonce,
      desktopNonce: desktopNonce,
    );
    final phone = deriveSession(
      shared: beforeNm(hostKeys.publicKey, clientKeys.secretKey),
      transcript: transcript,
      clientNonce: clientNonce,
      desktopNonce: desktopNonce,
    ).asPhone();
    expect(hexOf(host.recvKey), hexOf(phone.sendKey));

    final frames = <Uint8List>[];
    for (var i = 0; i < 3; i++) {
      final payload = Uint8List.fromList(utf8.encode('{"n":$i}'));
      final frame = phone.seal(
        payload,
        direction: dirMobileToDesktop,
        kind: kindText,
      );
      frames.add(frame);
      expect(
        utf8.decode(
          host.open(frame, direction: dirMobileToDesktop, kind: kindText),
        ),
        '{"n":$i}',
      );
    }
    // 重放旧帧要被拒绝
    expect(
      () => host.open(frames[0], direction: dirMobileToDesktop, kind: kindText),
      throwsA(isA<E2eeException>()),
    );

    for (final kind in [kindText, kindBinary, kindText]) {
      final frame = host.seal(
        Uint8List.fromList(utf8.encode('hi')),
        direction: dirDesktopToMobile,
        kind: kind,
      );
      final plain = phone.open(
        frame,
        direction: dirDesktopToMobile,
        kind: kind,
      );
      expect(utf8.decode(plain), 'hi');
    }
  });

  test('ready rejects extra keys and relay', () {
    final hello = Hello(
      clientPublicKey: repeated(1, 32),
      clientNonce: repeated(2, 32),
      context: const HelloContext(
        protocol: protocolName,
        initiator: 'mobile',
        responder: 'desktop',
        transport: 'direct',
      ),
    );
    final good = Ready(
      desktopPublicKey: repeated(3, 32),
      clientNonce: hello.clientNonce,
      desktopNonce: repeated(4, 32),
      context: hello.context,
    );
    expect(parseReady(jsonEncode(good.toJson()), hello), isNotNull);
    final extra = good.toJson()..['x'] = 1;
    expect(parseReady(jsonEncode(extra), hello), isNull);
    final relay = Hello(
      clientPublicKey: hello.clientPublicKey,
      clientNonce: hello.clientNonce,
      context: const HelloContext(
        protocol: protocolName,
        initiator: 'mobile',
        responder: 'desktop',
        transport: 'relay',
        relayHostId: 'AbCdEf0123_-xyZ9',
      ),
    );
    final relayReady = Ready(
      desktopPublicKey: repeated(3, 32),
      clientNonce: relay.clientNonce,
      desktopNonce: repeated(4, 32),
      context: relay.context,
    );
    expect(parseReady(jsonEncode(relayReady.toJson()), relay), isNull);
  });

  test('ready must select the framing the hello offered', () {
    final hello = Hello(
      clientPublicKey: repeated(1, 32),
      clientNonce: repeated(2, 32),
      context: const HelloContext(
        protocol: protocolName,
        initiator: 'mobile',
        responder: 'desktop',
        transport: 'direct',
      ),
    );
    expect(hello.toJson()['capabilities'], containsPair('framing', [3]));
    Ready ready(int framing) => Ready(
      desktopPublicKey: repeated(3, 32),
      clientNonce: hello.clientNonce,
      desktopNonce: repeated(4, 32),
      context: hello.context,
      framing: framing,
    );
    expect(
      parseReady(jsonEncode(ready(framingBinary).toJson()), hello)?.framing,
      framingBinary,
    );
    expect(parseReady(jsonEncode(ready(2).toJson()), hello), isNull);
  });

  test('a frame adds 17 bytes', () {
    final s = E2eeSession(
      recvKey: repeated(1, 32),
      sendKey: repeated(2, 32),
      id: repeated(9, 32),
      transcriptHash: Uint8List(32),
    );
    final frame = s.seal(
      Uint8List(10),
      direction: dirDesktopToMobile,
      kind: kindBinary,
    );
    expect(frame.length - 10, 17);
  });

  test(
    'binary framing rejects replays, gaps and short frames without moving on',
    () {
      E2eeSession make() => E2eeSession(
        recvKey: repeated(1, 32),
        sendKey: repeated(2, 32),
        id: repeated(9, 32),
        transcriptHash: Uint8List(32),
      );
      final host = make();
      final phone = host.asPhone();
      Uint8List next(String text) => host.seal(
        Uint8List.fromList(utf8.encode(text)),
        direction: dirDesktopToMobile,
        kind: kindText,
      );
      String open(Uint8List frame) => utf8.decode(
        phone
            .openMessage(frame, binary: true, direction: dirDesktopToMobile)
            .$2,
      );
      final first = next('a');
      final second = next('b');
      final bad = [
        second, // 跳号
        Uint8List.fromList([kindText, ...List.filled(16, 0)]), // 恰好只有标签
        Uint8List.fromList([kindText, 1, 2, 3]), // 不足标签长度
        Uint8List.fromList([kindText]),
        Uint8List(0),
      ];
      for (final frame in bad) {
        expect(() => open(frame), throwsA(isA<E2eeException>()));
      }
      expect(phone.recvN, 0);
      expect(open(first), 'a');
      expect(() => open(first), throwsA(isA<E2eeException>())); // 重放
      expect(open(second), 'b');
    },
  );

  test('binary framing tells messages from binary frames by the kind byte', () {
    E2eeSession make() => E2eeSession(
      recvKey: repeated(1, 32),
      sendKey: repeated(2, 32),
      id: repeated(9, 32),
      transcriptHash: Uint8List(32),
    );
    final host = make();
    final phone = host.asPhone();
    for (final kind in [kindText, kindBinary, kindBinary, kindText]) {
      final frame = host.seal(
        Uint8List.fromList(utf8.encode('k$kind')),
        direction: dirDesktopToMobile,
        kind: kind,
      );
      final (got, plain) = phone.openMessage(
        frame,
        binary: true,
        direction: dirDesktopToMobile,
      );
      expect(got, kind);
      expect(utf8.decode(plain), 'k$kind');
    }
    // 改了 nonce 里的 kind：nonce 对不上，解不开
    final frame = host.seal(
      Uint8List.fromList([1]),
      direction: dirDesktopToMobile,
      kind: kindText,
    );
    frame[0] = kindBinary;
    expect(
      () =>
          phone.openMessage(frame, binary: true, direction: dirDesktopToMobile),
      throwsA(isA<E2eeException>()),
    );
    // 不收文本帧
    final next = host.seal(
      Uint8List.fromList([2]),
      direction: dirDesktopToMobile,
      kind: kindText,
    );
    expect(
      () =>
          phone.openMessage(next, binary: false, direction: dirDesktopToMobile),
      throwsA(isA<E2eeException>()),
    );
  });
}
