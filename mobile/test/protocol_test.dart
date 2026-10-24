import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/app_state.dart';
import 'package:hive_mobile/protocol/e2ee.dart';
import 'package:hive_mobile/protocol/link.dart';
import 'package:hive_mobile/protocol/pairing.dart';
import 'package:hive_mobile/protocol/quic_socket.dart';
import 'package:hive_mobile/protocol/race.dart';
import 'package:hive_mobile/protocol/socket.dart';
import 'package:hive_mobile/workspace/workspace.dart';

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

  test('disconnecting stays disconnected until asked to connect', () async {
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
    var dials = 0;
    final model = HiveAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
      networkSignatures: const Stream.empty(),
      connect: (url, cancel) async {
        dials++;
        final (client, host) = pipePair();
        unawaited(_host(host, hostKeys, 'tok'));
        return client;
      },
    );
    expect(await model.connectCode(code), isTrue);
    await model.disconnect();
    expect(model.rpc, isNull);
    expect(model.disconnected, isTrue);
    // 配对留着，也不自己重连
    expect(model.computers, hasLength(1));
    final before = dials;
    await Future<void>.delayed(const Duration(milliseconds: 500));
    expect(dials, before);
    expect(model.rpc, isNull);
    // 假电脑回过 status.get 就不再回话，只等连上，不等工作区列表
    unawaited(model.retry());
    for (var i = 0; i < 100 && model.rpc == null; i++) {
      await Future<void>.delayed(const Duration(milliseconds: 50));
    }
    expect(model.rpc, isNotNull);
    expect(model.disconnected, isFalse);
  });

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

  group('permission prompts on first pairing', () {
    String codeFor(KeyPair keys) => base64Url.encode(
      utf8.encode(
        jsonEncode({
          'v': 2,
          'endpoint': 'only',
          'deviceToken': 'tok',
          'publicKeyB64': b64(keys.publicKey),
        }),
      ),
    );

    // iOS 弹网络权限窗时连接直接失败；用户点了允许，不用再扫一次
    test('keeps trying until the prompt is answered', () async {
      final hostKeys = generateKeyPair();
      var attempts = 0;
      final model =
          HiveAppModel(
              store: MemoryPairingStore(),
              settingsStore: MemorySettingsStore(),
              networkSignatures: const Stream.empty(),
              connect: (url, cancel) async {
                if (attempts++ < 3) {
                  throw const SocketException('No route to host');
                }
                final (client, host) = pipePair();
                unawaited(_host(host, hostKeys, 'tok'));
                return client;
              },
            )
            ..inForeground = (() => true)
            ..firstConnectRetry = const Duration(milliseconds: 10);
      expect(await model.connectCode(codeFor(hostKeys)), isTrue);
      expect(attempts, 4);
      expect(model.rpc, isNotNull);
      await model.link?.close();
    });

    test('gives up after the window and mentions permissions', () async {
      final hostKeys = generateKeyPair();
      final model =
          HiveAppModel(
              store: MemoryPairingStore(),
              settingsStore: MemorySettingsStore(),
              networkSignatures: const Stream.empty(),
              connect: (url, cancel) async =>
                  throw const SocketException('No route to host'),
            )
            ..inForeground = (() => true)
            ..firstConnectRetry = const Duration(milliseconds: 10)
            ..firstConnectWindow = const Duration(milliseconds: 100);
      expect(await model.connectCode(codeFor(hostKeys)), isFalse);
      expect(model.error, contains('本地网络'));
    });

    // 用户盯着权限弹窗看了很久：不在前台的时间不算，点完回来照样连上
    test('time spent on the prompt does not count', () async {
      final hostKeys = generateKeyPair();
      var prompt = true;
      final model =
          HiveAppModel(
              store: MemoryPairingStore(),
              settingsStore: MemorySettingsStore(),
              networkSignatures: const Stream.empty(),
              connect: (url, cancel) async {
                if (prompt) throw const SocketException('No route to host');
                final (client, host) = pipePair();
                unawaited(_host(host, hostKeys, 'tok'));
                return client;
              },
            )
            ..inForeground = (() => !prompt)
            ..firstConnectRetry = const Duration(milliseconds: 10)
            ..firstConnectWindow = const Duration(milliseconds: 100);
      final connected = model.connectCode(codeFor(hostKeys));
      await Future<void>.delayed(const Duration(milliseconds: 300));
      prompt = false;
      expect(await connected, isTrue);
      await model.link?.close();
    });

    // 电脑回了话但对不上（配对码里的主机密钥和它的不一样），重试也没用
    test('a key mismatch is not retried', () async {
      final hostKeys = generateKeyPair();
      var attempts = 0;
      final model =
          HiveAppModel(
              store: MemoryPairingStore(),
              settingsStore: MemorySettingsStore(),
              networkSignatures: const Stream.empty(),
              connect: (url, cancel) async {
                attempts++;
                final (client, host) = pipePair();
                unawaited(_host(host, hostKeys, 'tok'));
                return client;
              },
            )
            ..inForeground = (() => true)
            ..firstConnectRetry = const Duration(milliseconds: 10);
      expect(await model.connectCode(codeFor(generateKeyPair())), isFalse);
      expect(attempts, 1);
    });

    test('a saved computer is tried once', () async {
      final hostKeys = generateKeyPair();
      final code = codeFor(hostKeys);
      final store = MemoryPairingStore();
      var down = false;
      var attempts = 0;
      final model =
          HiveAppModel(
              store: store,
              settingsStore: MemorySettingsStore(),
              networkSignatures: const Stream.empty(),
              connect: (url, cancel) async {
                attempts++;
                if (down) throw const SocketException('Connection refused');
                final (client, host) = pipePair();
                unawaited(_host(host, hostKeys, 'tok'));
                return client;
              },
            )
            ..inForeground = (() => true)
            ..firstConnectRetry = const Duration(milliseconds: 10);
      expect(await model.connectCode(code), isTrue);
      await model.link?.close();
      down = true;
      attempts = 0;
      expect(await model.connectCode(code), isFalse);
      expect(attempts, 1);
    });
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
  group('quic', () {
    final hostKeys = generateKeyPair();
    final offer = PairingOffer(
      endpoint: 'ws://mac',
      endpoints: const [],
      deviceToken: 'tok',
      publicKeyB64: b64(hostKeys.publicKey),
    );
    const quicInfo = {
      'port': 6788,
      'hosts': ['10.0.0.2'],
      'relay': '203.0.113.9:443',
      'relayToken': 'AAECAwQFBgcICQoLDA0ODw==',
    };
    const lanUrl = 'hquic://10.0.0.2:6788';

    test('quic urls list the lan hosts first and the relay last', () {
      expect(quicUrlsOf(quicInfo), [
        lanUrl,
        'hquic://203.0.113.9:443?relay=AAECAwQFBgcICQoLDA0ODw==',
      ]);
      expect(isRelayQuicUrl(quicUrlsOf(quicInfo).last), isTrue);
      expect(isRelayQuicUrl(lanUrl), isFalse);
      expect(quicUrlsOf(const {}), isEmpty);
      expect(hostOf(lanUrl), '10.0.0.2:6788 · QUIC');
      expect(hostOf(quicUrlsOf(quicInfo).last), '经中转 · QUIC');
      expect(hostOf('ws://10.0.0.2:6788'), '10.0.0.2:6788');
    });

    test(
      'a websocket connection moves to quic once the host offers it',
      () async {
        final hosts = <String, ByteSocket>{};
        final link = HiveLink(
          offer: offer,
          connect: (url, cancel) async {
            if (url.contains('203.0.113.9')) throw StateError('relay down');
            final (client, host) = pipePair();
            hosts[url] = host;
            unawaited(_host(host, hostKeys, offer.deviceToken, quic: quicInfo));
            return client;
          },
        );
        final first = await link.start();
        expect(link.url, 'ws://mac');
        final next = await link.connections.first.timeout(
          const Duration(seconds: 2),
        );
        expect(identical(next, first), isFalse);
        expect(link.url, lanUrl);
        expect(link.quicUrls.first, lanUrl);
        final status = await next.call('status.get', {});
        expect(status['protocolVersion'], 3);
        await link.close();
      },
    );

    test(
      'a call in flight on the websocket survives the move to quic',
      () async {
        final link = HiveLink(
          offer: offer,
          connect: (url, cancel) async {
            if (url.contains('203.0.113.9')) throw StateError('relay down');
            final (client, host) = pipePair();
            unawaited(_host(host, hostKeys, offer.deviceToken, quic: quicInfo));
            return client;
          },
        );
        final first = await link.start();
        final moved = link.connections.first;
        final slow = first.call('test.slow', const {});
        await moved.timeout(const Duration(seconds: 2));
        expect(link.url, lanUrl);
        await expectLater(slow, completes);
        await link.close();
      },
    );

    test('the old connection stays until its subscriptions move', () async {
      final link = HiveLink(
        offer: offer,
        connect: (url, cancel) async {
          if (url.contains('203.0.113.9')) throw StateError('relay down');
          final (client, host) = pipePair();
          unawaited(_host(host, hostKeys, offer.deviceToken, quic: quicInfo));
          return client;
        },
      );
      final first = await link.start();
      final sub = await first.subscribe('terminal.subscribe', const {});
      var closed = false;
      sub.frames.listen((_) {}, onDone: () => closed = true);
      await link.connections.first.timeout(const Duration(seconds: 2));
      // 页面还没退掉旧订阅：旧连接不关，电脑那边终端不会先退回电脑尺寸
      await Future<void>.delayed(const Duration(milliseconds: 800));
      expect(closed, isFalse);
      await expectLater(first.call('runtime.ping', const {}), completes);
      await sub.cancel();
      await Future<void>.delayed(const Duration(milliseconds: 300));
      await expectLater(
        first.call('runtime.ping', const {}),
        throwsA(anything),
      );
      await link.close();
    });

    test('saved quic urls connect straight away and get refreshed', () async {
      final tried = <String>[];
      final learned = <List<String>>[];
      final link =
          HiveLink(
              offer: offer,
              connect: (url, cancel) async {
                tried.add(url);
                if (url.contains('203.0.113.9')) throw StateError('relay down');
                final (client, host) = pipePair();
                unawaited(
                  _host(host, hostKeys, offer.deviceToken, quic: quicInfo),
                );
                return client;
              },
            )
            ..quicUrls = const [lanUrl]
            ..onQuicUrls = learned.add;
      await link.start();
      expect(link.url, lanUrl);
      await Future<void>.delayed(const Duration(milliseconds: 100));
      // WebSocket 晚 2 秒才起跑，QUIC 先连上就不用它
      expect(tried, [lanUrl]);
      expect(learned.single, quicUrlsOf(quicInfo));
      await link.close();
    });

    test(
      'quic urls are saved with the computer for the next cold start',
      () async {
        final code = base64Url.encode(
          utf8.encode(
            jsonEncode({
              'v': 2,
              'endpoint': 'ws://mac',
              'deviceToken': 'tok',
              'publicKeyB64': b64(hostKeys.publicKey),
            }),
          ),
        );
        final store = MemoryPairingStore();
        final tried = <String>[];
        HiveAppModel model() => HiveAppModel(
          store: store,
          settingsStore: MemorySettingsStore(),
          networkSignatures: const Stream.empty(),
          connect: (url, cancel) async {
            tried.add(url);
            if (url.contains('203.0.113.9')) throw StateError('relay down');
            final (client, host) = pipePair();
            unawaited(_host(host, hostKeys, 'tok', quic: quicInfo));
            return client;
          },
        );
        final first = model();
        expect(await first.connectCode(code), isTrue);
        for (var i = 0; i < 40 && first.connectedUrl != lanUrl; i++) {
          await Future<void>.delayed(const Duration(milliseconds: 50));
        }
        expect(first.connectedUrl, lanUrl);
        await first.link?.close();
        expect(
          SavedComputers.decode(await store.read()).current?.quic,
          quicUrlsOf(quicInfo),
        );

        tried.clear();
        final second = model();
        await second.restore();
        expect(tried.first, lanUrl);
        expect(second.connectedUrl, lanUrl);
        await second.link?.close();
      },
      // 测试用的电脑回完 status.get 就不再应答，首页的轮询每次连上都要等满 20 秒才超时
      timeout: const Timeout(Duration(seconds: 90)),
    );

    test('a relay connection punches through to a direct one', () async {
      final relayUrl = quicUrlsOf(quicInfo).last;
      String? told;
      final link =
          HiveLink(
              offer: offer,
              connect: (url, cancel) async {
                if (url == lanUrl) throw StateError('not home');
                final (client, host) = pipePair();
                unawaited(
                  _host(host, hostKeys, offer.deviceToken, quic: quicInfo),
                );
                return client;
              },
            )
            ..quicUrls = quicUrlsOf(quicInfo)
            ..punch = (relay, exchange) async {
              expect(relay, relayUrl);
              // 电脑那边经 hive.punch 换回它的地址；测试用的电脑把这个方法当成未知方法回空结果
              told = 'asked';
              await exchange('198.51.100.7:5000').catchError((Object _) => '');
              final (client, host) = pipePair();
              unawaited(
                _host(host, hostKeys, offer.deviceToken, quic: quicInfo),
              );
              return (client as ByteSocket, '203.0.113.20:61000');
            };
      final relayed = await link.start();
      expect(link.url, relayUrl);
      final moved = await link.connections.first.timeout(
        const Duration(seconds: 3),
      );
      expect(told, 'asked');
      expect(identical(moved, relayed), isFalse);
      expect(link.url, punchedUrl('203.0.113.20:61000'));
      expect(hostOf(link.url), '直连 · QUIC');
      expect(isRelayQuicUrl(link.url!), isFalse);
      await expectLater(moved.call('runtime.ping', const {}), completes);
      await link.close();
    });

    test('a failed punch stays on the relay', () async {
      final relayUrl = quicUrlsOf(quicInfo).last;
      var punches = 0;
      final link =
          HiveLink(
              offer: offer,
              connect: (url, cancel) async {
                if (url == lanUrl) throw StateError('not home');
                final (client, host) = pipePair();
                unawaited(
                  _host(host, hostKeys, offer.deviceToken, quic: quicInfo),
                );
                return client;
              },
            )
            ..quicUrls = quicUrlsOf(quicInfo)
            ..punch = (relay, exchange) async {
              punches++;
              throw StateError('symmetric nat');
            };
      final relayed = await link.start();
      await Future<void>.delayed(const Duration(milliseconds: 200));
      expect(punches, 1);
      expect(identical(link.rpc, relayed), isTrue);
      expect(link.url, relayUrl);
      await link.close();
    });

    test('a failed upgrade keeps the websocket and its reconnects', () async {
      var quicTries = 0;
      final wsHosts = <ByteSocket>[];
      final link = HiveLink(
        offer: offer,
        connect: (url, cancel) async {
          if (url.startsWith('hquic')) {
            quicTries++;
            throw StateError('udp blocked');
          }
          final (client, host) = pipePair();
          wsHosts.add(host);
          unawaited(_host(host, hostKeys, offer.deviceToken, quic: quicInfo));
          return client;
        },
      )..quicHeadStart = const Duration(milliseconds: 100);
      final first = await link.start();
      await Future<void>.delayed(const Duration(milliseconds: 100));
      expect(quicTries, 2);
      expect(identical(link.rpc, first), isTrue);
      expect(link.url, 'ws://mac');

      // 电脑那头断开：先试 QUIC，晚 0.1 秒再连 WebSocket，连上后又问一次 QUIC
      final reconnected = link.connections.first;
      await wsHosts.single.close();
      final sw = Stopwatch()..start();
      await reconnected.timeout(const Duration(seconds: 3));
      expect(
        sw.elapsed,
        greaterThanOrEqualTo(const Duration(milliseconds: 100)),
      );
      expect(link.url, 'ws://mac');
      await Future<void>.delayed(const Duration(milliseconds: 100));
      expect(quicTries, 6);
      await link.close();
    });

    test('a host without hive.quic stays on websocket', () async {
      var quicTries = 0;
      final link = HiveLink(
        offer: offer,
        connect: (url, cancel) async {
          if (url.startsWith('hquic')) quicTries++;
          final (client, host) = pipePair();
          unawaited(_host(host, hostKeys, offer.deviceToken));
          return client;
        },
      );
      await link.start();
      await Future<void>.delayed(const Duration(milliseconds: 100));
      expect(quicTries, 0);
      expect(link.quicUrls, isEmpty);
      expect(link.url, 'ws://mac');
      await link.close();
    });
  });
}

Future<void> _host(
  ByteSocket socket,
  KeyPair hostKeys,
  String token, {
  bool rejectCapabilities = false,
  bool silent = false,
  Map<String, Object?>? quic,
}) async {
  try {
    await _serve(
      socket,
      hostKeys,
      token,
      rejectCapabilities: rejectCapabilities,
      silent: silent,
      quic: quic,
    );
  } on E2eeException {
    // 对端挂断
  }
}

/// [silent]：握手之后不回 runtime.ping，模拟手机切网络后系统还没发现的半开连接。
/// [quic]：hive.quic 的回复，为空时和 Orca 一样不认这个方法。
Future<void> _serve(
  ByteSocket socket,
  KeyPair hostKeys,
  String token, {
  bool rejectCapabilities = false,
  bool silent = false,
  Map<String, Object?>? quic,
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
    if (method == 'test.slow') {
      await Future<void>.delayed(const Duration(milliseconds: 300));
    }
    if (method == 'hive.quic') {
      if (quic != null) {
        result = quic;
      } else {
        ok = false;
        error = {'code': 'method_not_found', 'message': 'Unknown method'};
      }
    }
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
