import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/app_state.dart';
import 'package:hive_mobile/protocol/e2ee.dart';
import 'package:hive_mobile/protocol/link.dart';
import 'package:hive_mobile/protocol/pairing.dart';
import 'package:hive_mobile/protocol/race.dart';
import 'package:hive_mobile/protocol/socket.dart';

void main() {
  test('pairing offer keeps endpoint first and parses the code', () {
    final payload = {
      'v': 2,
      'endpoint': 'ws://127.0.0.1:6788',
      'endpoints': [
        'ws://127.0.0.1:6788',
        'ws://100.1.2.3:6788',
        'wss://tun.example/hive',
      ],
      'deviceToken': 'tok',
      'publicKeyB64': b64(repeated(9, 32)),
      'pairedDeviceId': 'dev',
      'scope': 'mobile',
    };
    final code = base64Url
        .encode(utf8.encode(jsonEncode(payload)))
        .replaceAll('=', '');
    final offer = PairingOffer.parse('orca://pair?code=$code');
    expect(offer.endpoint, 'ws://127.0.0.1:6788');
    expect(offer.dialUrls, [
      'ws://127.0.0.1:6788',
      'ws://100.1.2.3:6788',
      'wss://tun.example/hive',
    ]);
    expect(offer.deviceToken, 'tok');
  });

  test('happy eyeballs keeps the first ready handshake', () async {
    final abandoned = <String>[];
    final result = await raceEndpoints<String>(
      urls: ['slow', 'fast', 'fail'],
      attempt: (url, cancel) async {
        if (url == 'fail') throw StateError('down');
        if (url == 'slow') {
          await Future<void>.delayed(const Duration(milliseconds: 30));
          if (cancel.cancelled) throw const RaceCancelled();
          return 'slow-socket';
        }
        return 'fast-socket';
      },
      abandon: (value) async => abandoned.add(value),
    );
    expect(result.url, 'fast');
    expect(result.value, 'fast-socket');
    await Future<void>.delayed(const Duration(milliseconds: 50));
    expect(abandoned, isEmpty);
  });

  test('a late success is abandoned after cancel', () async {
    final abandoned = <String>[];
    final result = await raceEndpoints<String>(
      urls: ['fast', 'slow'],
      attempt: (url, cancel) async {
        if (url == 'slow') {
          await Future<void>.delayed(const Duration(milliseconds: 20));
          return 'slow-socket';
        }
        return 'fast-socket';
      },
      abandon: (value) async => abandoned.add(value),
    );
    expect(result.value, 'fast-socket');
    await Future<void>.delayed(const Duration(milliseconds: 40));
    expect(abandoned, ['slow-socket']);
  });

  test('network gate ignores the first signature', () {
    final gate = NetworkGate();
    expect(gate.shouldReconnect('wifi'), isFalse);
    expect(gate.shouldReconnect('wifi'), isFalse);
    expect(gate.shouldReconnect('cell'), isTrue);
  });

  test(
    'handshake declares capabilities and reconnects on a network change',
    () async {
      final hostKeys = generateKeyPair();
      final offer = PairingOffer(
        endpoint: 'fast',
        endpoints: const ['slow', 'fast'],
        deviceToken: 'tok',
        publicKeyB64: b64(hostKeys.publicKey),
      );
      final networks = StreamController<String>();
      var connects = 0;
      final link = HiveLink(
        offer: offer,
        networkSignatures: networks.stream,
        connect: (url, cancel) async {
          connects++;
          if (url == 'slow') {
            await Future<void>.delayed(const Duration(milliseconds: 200));
            if (cancel.cancelled) throw const RaceCancelled();
          }
          final (client, host) = pipePair();
          unawaited(_host(host, hostKeys, offer.deviceToken));
          return client;
        },
      );
      final rpc = await link.start();
      expect(connects, 2);
      // 记下的是先握手成功的那个地址，界面上显示它
      expect(link.url, 'fast');
      final status = await rpc.call('status.get', {});
      expect(status['protocolVersion'], 3);
      final seen = link.connections.first;
      networks.add('wifi');
      networks.add('wifi');
      networks.add('cell');
      await seen.timeout(const Duration(seconds: 2));
      expect(connects, greaterThan(2));
      await link.close();
      await networks.close();
    },
  );

  test('a link that stops answering pings is dropped and reopened', () async {
    final hostKeys = generateKeyPair();
    final offer = PairingOffer(
      endpoint: 'only',
      endpoints: const [],
      deviceToken: 'tok',
      publicKeyB64: b64(hostKeys.publicKey),
    );
    var connects = 0;
    final link =
        HiveLink(
            offer: offer,
            connect: (url, cancel) async {
              connects++;
              final (client, host) = pipePair();
              // 第一条连接握手后就不出声了，socket 却一直开着
              unawaited(_host(host, hostKeys, 'tok', silent: connects == 1));
              return client;
            },
          )
          ..latencyInterval = const Duration(milliseconds: 30)
          ..latencyTimeout = const Duration(milliseconds: 60);
    final first = await link.start();
    final next = await link.connections.first.timeout(
      const Duration(seconds: 3),
    );
    expect(identical(next, first), isFalse);
    expect(connects, 2);
    // 新连接上回得了 ping，延迟测得出来
    await Future<void>.delayed(const Duration(milliseconds: 200));
    expect(link.meter?.latency, isNotNull);
    expect(link.meter?.unresponsive, isFalse);
    await link.close();
  });

  test('a signature during the first handshake does not cancel it', () async {
    final hostKeys = generateKeyPair();
    final offer = PairingOffer(
      endpoint: 'only',
      endpoints: const [],
      deviceToken: 'tok',
      publicKeyB64: b64(hostKeys.publicKey),
    );
    final networks = StreamController<String>();
    // 握手还没开始就有两次签名。旧实现会在 listen 里立刻重连，把第一轮顶掉。
    networks.add('wifi');
    networks.add('cell');
    var connects = 0;
    final link = HiveLink(
      offer: offer,
      networkSignatures: networks.stream,
      connect: (url, cancel) async {
        connects++;
        final (client, host) = pipePair();
        unawaited(_host(host, hostKeys, offer.deviceToken));
        return client;
      },
    );
    await link.start();
    expect(connects, 1);
    final status = await link.rpc!.call('status.get', {});
    expect(status['protocolVersion'], 3);
    await link.close();
    await networks.close();
  });

  test(
    'an old daemon without capability update still finishes the handshake',
    () async {
      final hostKeys = generateKeyPair();
      final offer = PairingOffer(
        endpoint: 'only',
        endpoints: const [],
        deviceToken: 'tok',
        publicKeyB64: b64(hostKeys.publicKey),
      );
      final link = HiveLink(
        offer: offer,
        connect: (url, cancel) async {
          final (client, host) = pipePair();
          unawaited(
            _host(host, hostKeys, offer.deviceToken, rejectCapabilities: true),
          );
          return client;
        },
      );
      final rpc = await link.start();
      final status = await rpc.call('status.get', {});
      expect(status['protocolVersion'], 3);
      await link.close();
    },
  );

  test('a failed reconnect keeps the live connection', () async {
    final hostKeys = generateKeyPair();
    final offer = PairingOffer(
      endpoint: 'fast',
      endpoints: const ['slow', 'fast'],
      deviceToken: 'tok',
      publicKeyB64: b64(hostKeys.publicKey),
    );
    final networks = StreamController<String>();
    var fail = false;
    var connects = 0;
    final link = HiveLink(
      offer: offer,
      networkSignatures: networks.stream,
      connect: (url, cancel) async {
        connects++;
        if (fail) throw StateError('down');
        if (url == 'slow') {
          await Future<void>.delayed(const Duration(milliseconds: 200));
          if (cancel.cancelled) throw const RaceCancelled();
        }
        final (client, host) = pipePair();
        unawaited(_host(host, hostKeys, offer.deviceToken));
        return client;
      },
    );
    final rpc = await link.start();
    expect(connects, 2);
    fail = true;
    networks.add('wifi');
    networks.add('cell');
    await Future<void>.delayed(const Duration(milliseconds: 100));
    expect(identical(link.rpc, rpc), isTrue);
    final status = await rpc.call('status.get', {});
    expect(status['protocolVersion'], 3);
    final beforeClose = connects;
    await rpc.close();
    await Future<void>.delayed(const Duration(milliseconds: 500));
    expect(connects, beforeClose);
    await link.close();
    await networks.close();
  });

  test(
    'a dropped connection is cleared and retried until it comes back',
    () async {
      final hostKeys = generateKeyPair();
      final offer = PairingOffer(
        endpoint: 'only',
        endpoints: const [],
        deviceToken: 'tok',
        publicKeyB64: b64(hostKeys.publicKey),
      );
      final hosts = <ByteSocket>[];
      var failures = 0;
      var changes = 0;
      final link = HiveLink(
        offer: offer,
        connect: (url, cancel) async {
          if (failures > 0) {
            failures--;
            throw StateError('down');
          }
          final (client, host) = pipePair();
          hosts.add(host);
          unawaited(_host(host, hostKeys, offer.deviceToken));
          return client;
        },
      );
      link.onActivity = () => changes++;
      final first = await link.start();
      // 电脑那头断了，前两次重连也失败（常驻进程还在重启）
      failures = 2;
      await hosts.single.close();
      await Future<void>.delayed(const Duration(milliseconds: 100));
      expect(link.rpc, isNull);
      expect(changes, greaterThan(0));
      final back = await link.connections.first.timeout(
        const Duration(seconds: 5),
      );
      expect(identical(back, first), isFalse);
      expect((await back.call('status.get', {}))['protocolVersion'], 3);
      await link.close();
    },
  );

  test('reconnecting clears the errors left by failed retries', () async {
    final hostKeys = generateKeyPair();
    final code = base64Url.encode(
      utf8.encode(
        jsonEncode({
          'v': 2,
          'endpoint': 'only',
          'deviceToken': 'tok',
          'publicKeyB64': b64(hostKeys.publicKey),
        }),
      ),
    );
    final hosts = <ByteSocket>[];
    var failures = 0;
    final model = HiveAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
      networkSignatures: const Stream.empty(),
      connect: (url, cancel) async {
        if (failures > 0) {
          failures--;
          throw StateError('down');
        }
        final (client, host) = pipePair();
        hosts.add(host);
        unawaited(_host(host, hostKeys, 'tok'));
        return client;
      },
    );
    expect(await model.connectCode(code), isTrue);

    Future<void> until(bool Function() done) async {
      for (var i = 0; i < 100 && !done(); i++) {
        await Future<void>.delayed(const Duration(milliseconds: 50));
      }
      expect(done(), isTrue);
    }

    // 常驻进程重启：断开，前两次重连失败，留下报错
    failures = 2;
    await hosts.last.close();
    await until(() => model.rpc == null && model.error != null);
    // 连上之后，首页不能有哪一刻是"连着却还挂着报错"（那会闪一下红色胶囊）
    var stale = false;
    model.addListener(() {
      if (model.rpc != null && !model.connecting && model.error != null) {
        stale = true;
      }
    });
    await until(() => model.rpc != null);
    await Future<void>.delayed(const Duration(milliseconds: 200));
    expect(stale, isFalse);
    expect(model.error, isNull);
    await model.link?.close();
  });

  test('an address that never answers does not stall reconnecting', () async {
    final hostKeys = generateKeyPair();
    final offer = PairingOffer(
      endpoint: 'good',
      endpoints: const ['hang'],
      deviceToken: 'tok',
      publicKeyB64: b64(hostKeys.publicKey),
    );
    final hosts = <ByteSocket>[];
    var down = false;
    final link = HiveLink(
      offer: offer,
      attemptTimeout: const Duration(milliseconds: 200),
      connect: (url, cancel) async {
        // 这个地址既不通也不报错，只能靠超时放弃
        if (url == 'hang') return Completer<ByteSocket>().future;
        if (down) throw StateError('refused');
        final (client, host) = pipePair();
        hosts.add(host);
        unawaited(_host(host, hostKeys, offer.deviceToken));
        return client;
      },
    );
    await link.start();
    down = true;
    await hosts.single.close();
    await Future<void>.delayed(const Duration(milliseconds: 500));
    expect(link.rpc, isNull);
    // 电脑回来了：挂着的地址每轮 200ms 超时，重连照样接上
    down = false;
    final back = await link.connections.first.timeout(
      const Duration(seconds: 5),
    );
    expect((await back.call('status.get', {}))['protocolVersion'], 3);
    await link.close();
  });
}

/// 假电脑。手机挂断时读者报 socket closed，对它来说就是正常结束。
Future<void> _host(
  ByteSocket socket,
  KeyPair hostKeys,
  String token, {
  bool rejectCapabilities = false,
  bool silent = false,
}) async {
  try {
    await _serve(
      socket,
      hostKeys,
      token,
      rejectCapabilities: rejectCapabilities,
      silent: silent,
    );
  } on E2eeException {
    // 对端挂断
  }
}

/// [silent]：握手之后不回 runtime.ping，模拟手机切网络后系统还没发现的半开连接。
Future<void> _serve(
  ByteSocket socket,
  KeyPair hostKeys,
  String token, {
  bool rejectCapabilities = false,
  bool silent = false,
}) async {
  final reader = SocketReader(socket.events);
  final helloEvent = await reader.next(CancelToken());
  final helloJson = jsonDecode(utf8.decode(helloEvent.bytes));
  if (helloJson is! Map) return;
  final clientPublic = decode32(helloJson['clientPublicKeyB64'])!;
  final clientNonce = decode32(helloJson['clientNonceB64'])!;
  const context = HelloContext(
    protocol: protocolName,
    initiator: 'mobile',
    responder: 'desktop',
    transport: 'direct',
  );
  final hello = Hello(
    clientPublicKey: clientPublic,
    clientNonce: clientNonce,
    context: context,
  );
  final desktopNonce = randomBytes(32);
  final ready = Ready(
    desktopPublicKey: hostKeys.publicKey,
    clientNonce: clientNonce,
    desktopNonce: desktopNonce,
    context: context,
  );
  await socket.sendText(jsonEncode(ready.toJson()));
  final session = deriveSession(
    shared: beforeNm(clientPublic, hostKeys.secretKey),
    transcript: encodeTranscript(hello, ready),
    clientNonce: clientNonce,
    desktopNonce: desktopNonce,
  );
  final authEvent = await reader.next(CancelToken());
  final authPlain = utf8.decode(
    session.openText(authEvent.bytes, direction: dirMobileToDesktop),
  );
  final auth = jsonDecode(authPlain);
  if (auth is! Map || auth['deviceToken'] != token) return;
  final ok = jsonEncode({
    'type': 'e2ee_authenticated',
    'v': 2,
    'transcriptHashB64': b64(session.transcriptHash),
  });
  await socket.sendText(
    base64.encode(
      session.seal(
        Uint8List.fromList(utf8.encode(ok)),
        direction: dirDesktopToMobile,
        kind: kindText,
      ),
    ),
  );
  while (true) {
    final event = await reader.next(CancelToken());
    final Map<String, Object?> request;
    try {
      final decoded = jsonDecode(
        utf8.decode(
          session.openText(event.bytes, direction: dirMobileToDesktop),
        ),
      );
      if (decoded is! Map) continue;
      request = decoded.cast<String, Object?>();
    } catch (_) {
      return;
    }
    final id = request['id'];
    final method = request['method'];
    if (silent && method == 'runtime.ping') continue;
    Object result = <String, Object?>{};
    var ok = true;
    Object? error;
    if (method == 'status.get') result = {'protocolVersion': 3};
    if (method == 'runtime.clientCapabilities.update' && rejectCapabilities) {
      ok = false;
      error = {'code': 'method_not_found', 'message': 'Unknown method'};
    }
    final reply = jsonEncode({
      'id': id,
      'ok': ok,
      'result': result,
      if (!ok) 'error': error,
    });
    await socket.sendText(
      base64.encode(
        session.seal(
          Uint8List.fromList(utf8.encode(reply)),
          direction: dirDesktopToMobile,
          kind: kindText,
        ),
      ),
    );
    if (method == 'status.get') return;
  }
}
