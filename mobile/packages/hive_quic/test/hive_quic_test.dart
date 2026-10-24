import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:hive_quic/hive_quic.dart';
import 'package:test/test.dart';

void main() {
  late Process echo;
  late int port;

  setUpAll(() async {
    final repo = Directory.current.uri.resolve('../../../').toFilePath();
    echo = await Process.start('go', ['run', './mobile/packages/hive_quic/test/echo'], workingDirectory: repo);
    unawaited(stderr.addStream(echo.stderr));
    final line = await echo.stdout.transform(utf8.decoder).transform(const LineSplitter()).first;
    port = int.parse(line.trim());
  });

  tearDownAll(() => echo.kill());

  test('round trips text and binary in order', () async {
    final conn = await QuicConnection.dial('127.0.0.1', port);
    final got = <QuicMessage>[];
    final done = Completer<void>();
    conn.messages.listen((m) {
      got.add(m);
      if (got.length == 101) done.complete();
    });
    final big = Uint8List(300 * 1024)..fillRange(0, 300 * 1024, 7);
    conn.send(quicBinary, big);
    for (var i = 0; i < 100; i++) {
      conn.send(quicText, Uint8List.fromList(utf8.encode('m$i')));
    }
    await done.future.timeout(const Duration(seconds: 10));
    expect(got.first.binary, isTrue);
    expect(got.first.data.length, big.length);
    expect([for (final m in got.skip(1)) utf8.decode(m.data)], [for (var i = 0; i < 100; i++) 'm$i']);
    conn.close();
  });

  test('peer close delivers the last message then ends', () async {
    final conn = await QuicConnection.dial('127.0.0.1', port);
    final msgs = <String>[];
    final ended = Completer<void>();
    conn.messages.listen((m) => msgs.add(utf8.decode(m.data)), onDone: ended.complete);
    conn.send(quicText, Uint8List.fromList(utf8.encode('close')));
    await ended.future.timeout(const Duration(seconds: 5));
    expect(msgs, ['bye']);
    expect(() => conn.send(quicText, Uint8List(1)), throwsA(isA<QuicException>()));
  });

  test('unanswered relay fails fast', () async {
    final sock = await RawDatagramSocket.bind('127.0.0.1', 0);
    final dead = sock.port;
    sock.close();
    final sw = Stopwatch()..start();
    await expectLater(
      QuicConnection.dial('127.0.0.1', dead, relayToken: Uint8List(16)),
      throwsA(isA<QuicException>()),
    );
    expect(sw.elapsed, lessThan(const Duration(seconds: 3)));
  });
}
