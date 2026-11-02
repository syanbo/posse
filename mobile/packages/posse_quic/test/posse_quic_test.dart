import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:posse_quic/posse_quic.dart';
import 'package:test/test.dart';

void main() {
  late Process echo;
  late int port;
  late int relayPort;
  late StreamIterator<String> lines;

  setUpAll(() async {
    final repo = Directory.current.uri.resolve('../../../').toFilePath();
    echo = await Process.start('go', [
      'run',
      './mobile/packages/posse_quic/test/echo',
    ], workingDirectory: repo);
    unawaited(stderr.addStream(echo.stderr));
    lines = StreamIterator(
      echo.stdout.transform(utf8.decoder).transform(const LineSplitter()),
    );
    await lines.moveNext();
    final ports = lines.current.trim().split(' ');
    port = int.parse(ports[0]);
    relayPort = int.parse(ports[1]);
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
    expect(
      [for (final m in got.skip(1)) utf8.decode(m.data)],
      [for (var i = 0; i < 100; i++) 'm$i'],
    );
    conn.close();
  });

  test('peer close delivers the last message then ends', () async {
    final conn = await QuicConnection.dial('127.0.0.1', port);
    final msgs = <String>[];
    final ended = Completer<void>();
    conn.messages.listen(
      (m) => msgs.add(utf8.decode(m.data)),
      onDone: ended.complete,
    );
    conn.send(quicText, Uint8List.fromList(utf8.encode('close')));
    await ended.future.timeout(const Duration(seconds: 5));
    expect(msgs, ['bye']);
    // 关闭码在消息流结束前就交过来了
    expect(conn.closeCode, 4001);
    expect(
      () => conn.send(quicText, Uint8List(1)),
      throwsA(isA<QuicException>()),
    );
  });

  test('punches through to the peer the relay saw', () async {
    final punch = await QuicConnection.prepare('127.0.0.1', relayPort);
    expect(punch.public, startsWith('127.0.0.1:'));
    echo.stdin.writeln('punch ${punch.public}');
    await lines.moveNext();
    final peer = lines.current.trim();
    expect(peer, isNot('127.0.0.1:$port'));
    final conn = await punch.dial(peer);
    final got = conn.messages.first;
    conn.send(quicText, Uint8List.fromList(utf8.encode('hi')));
    expect(
      utf8.decode((await got.timeout(const Duration(seconds: 5))).data),
      'hi',
    );
    conn.close();
  });

  test('a prepared socket can be dropped without dialing', () async {
    final punch = await QuicConnection.prepare('127.0.0.1', relayPort);
    punch.close();
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
