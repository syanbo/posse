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
    final hello = Hello(
      clientPublicKey: repeated(1, 32),
      clientNonce: repeated(2, 32),
      context: context,
    );
    final ready = Ready(
      desktopPublicKey: repeated(3, 32),
      clientNonce: repeated(2, 32),
      desktopNonce: repeated(4, 32),
      context: context,
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
    expect(
      hexOf(frame),
      '339ae1f2bdff63481857d2810201000000000000000000002f21aaab2d6989389cd450263b93b3c5bf64fd318b70ca4c2844bc702ffd9811d0dfa9fea3f633ad6d946eaba8e61619d0ebdf9658edc1d2f8ded559',
    );
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

    for (var i = 0; i < 3; i++) {
      final payload = Uint8List.fromList(utf8.encode('{"n":$i}'));
      final frame = phone.seal(
        payload,
        direction: dirMobileToDesktop,
        kind: kindText,
      );
      expect(
        utf8.decode(
          host.open(frame, direction: dirMobileToDesktop, kind: kindText),
        ),
        '{"n":$i}',
      );
    }
    final replay = secretboxSeal(
      phone.sendKey,
      frameNonce(phone.id, dirMobileToDesktop, kindText, 0),
      concat([
        frameHeader(phone.id, dirMobileToDesktop, kindText, 0),
        Uint8List.fromList([120]),
      ]),
    );
    expect(
      () => host.open(replay, direction: dirMobileToDesktop, kind: kindText),
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
}
