import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/desk/chrome.dart';
import 'package:posse_mobile/desk/direct.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/protocol/e2ee.dart';
import 'package:posse_mobile/protocol/link.dart';
import 'package:posse_mobile/protocol/pairing.dart';
import 'package:posse_mobile/protocol/quic_socket.dart';
import 'package:posse_mobile/protocol/race.dart';
import 'package:posse_mobile/protocol/socket.dart';
import 'package:posse_mobile/protocol/tailscale_service.dart';
import 'package:posse_mobile/workspace/workspace.dart';
import 'package:posse_quic/posse_quic.dart';

class _FakeHost {
  _FakeHost(this.keys, {this.quicInfo});

  final KeyPair keys;
  final Map<String, Object?>? quicInfo;
  bool answerPing = true;
  int pings = 0;

  ByteSocket accept() {
    final (client, host) = pipePair();
    unawaited(_serve(host));
    return client;
  }

  Future<void> _serve(ByteSocket host) async {
    final reader = SocketReader(host.events);
    try {
      final helloEv = await reader.next(CancelToken());
      final helloJson = jsonDecode(utf8.decode(helloEv.bytes)) as Map;
      final clientPub = Uint8List.fromList(
        base64.decode(helloJson['clientPublicKeyB64'] as String),
      );
      final clientNonce = Uint8List.fromList(
        base64.decode(helloJson['clientNonceB64'] as String),
      );
      const ctx = HelloContext(
        protocol: protocolName,
        initiator: 'mobile',
        responder: 'desktop',
        transport: 'direct',
      );
      final hello = Hello(
        clientPublicKey: clientPub,
        clientNonce: clientNonce,
        context: ctx,
      );
      final deskNonce = randomBytes(32);
      final ready = Ready(
        desktopPublicKey: keys.publicKey,
        clientNonce: clientNonce,
        desktopNonce: deskNonce,
        context: ctx,
      );
      await host.sendText(jsonEncode(ready.toJson()));
      final session = deriveSession(
        shared: beforeNm(clientPub, keys.secretKey),
        transcript: encodeTranscript(hello, ready),
        clientNonce: clientNonce,
        desktopNonce: deskNonce,
      );

      final authEv = await reader.next(CancelToken());
      session.openMessage(
        authEv.bytes,
        binary: authEv.binary,
        direction: dirMobileToDesktop,
      );
      final authed = session.seal(
        Uint8List.fromList(
          utf8.encode(
            jsonEncode({
              'type': 'e2ee_authenticated',
              'v': 2,
              'transcriptHashB64': b64(session.transcriptHash),
            }),
          ),
        ),
        direction: dirDesktopToMobile,
        kind: kindText,
      );
      await host.sendBinary(authed);

      while (true) {
        final ev = await reader.next(CancelToken());
        final (_, plain) = session.openMessage(
          ev.bytes,
          binary: ev.binary,
          direction: dirMobileToDesktop,
        );
        final msg = jsonDecode(utf8.decode(plain)) as Map;
        final id = msg['id'];
        final method = msg['method'];
        if (method == 'runtime.ping') {
          pings++;
          if (!answerPing) continue;
          final reply = session.seal(
            Uint8List.fromList(
              utf8.encode(jsonEncode({'id': id, 'ok': true, 'result': const {}})),
            ),
            direction: dirDesktopToMobile,
            kind: kindText,
          );
          await host.sendBinary(reply);
          continue;
        }
        final result = switch (method) {
          'posse.quic' => quicInfo ?? const <String, Object?>{},
          _ => const <String, Object?>{},
        };
        final reply = session.seal(
          Uint8List.fromList(
            utf8.encode(jsonEncode({'id': id, 'ok': true, 'result': result})),
          ),
          direction: dirDesktopToMobile,
          kind: kindText,
        );
        await host.sendBinary(reply);
      }
    } catch (_) {}
  }
}

class _FakeTailscaleBackend implements TailscaleBackend {
  final _updates = StreamController<TailscaleStatus>.broadcast();
  TailscaleStatus current = const TailscaleStatus();
  int startCalls = 0;
  int wakeCalls = 0;
  final ops = <String>[];
  Completer<String>? stateDirGate;
  Future<TailscalePeerRoute> Function(String ip)? peerRouteHandler;
  TailscalePeerRoute route = const TailscalePeerRoute(kind: 'direct');

  void emit(TailscaleStatus st) {
    current = st;
    _updates.add(st);
  }

  @override
  Stream<TailscaleStatus> get updates => _updates.stream;

  @override
  TailscaleStatus status() => current;

  @override
  Future<String> stateDir() async {
    if (stateDirGate case final gate?) return gate.future;
    return '/tmp/fake-ts';
  }

  @override
  Future<TailscaleStatus> start({
    required String stateDir,
    String hostname = '',
    String controlUrl = '',
    String authKey = '',
    bool disableBindInterface = false,
  }) async {
    startCalls++;
    ops.add('start');
    current = TailscaleStatus(
      enabled: true,
      state: 'running',
      ip: '100.64.0.50',
      hostname: hostname,
      controlUrl: controlUrl,
      disableBindInterface: disableBindInterface,
    );
    _updates.add(current);
    return current;
  }

  @override
  Future<TailscaleStatus> stop() async {
    ops.add('stop');
    current = const TailscaleStatus(enabled: false, state: 'disabled');
    _updates.add(current);
    return current;
  }

  @override
  Future<TailscaleResetResult> reset({String stateDir = ''}) async {
    ops.add('reset');
    current = const TailscaleStatus(enabled: false, state: 'disabled');
    _updates.add(current);
    return TailscaleResetResult(
      status: current,
      loggedOut: true,
      logoutError: '',
      localCleared: true,
    );
  }

  @override
  Future<TailscaleStatus> startLogin() async => current;

  @override
  void wake() => wakeCalls++;

  @override
  Future<TailscalePeerRoute> peerRoute(String ip) =>
      peerRouteHandler?.call(ip) ?? Future.value(route);

  @override
  Future<TailscaleUdpBridge> openUdpBridge(String host, int port) {
    throw UnimplementedError();
  }
}

void main() {
  test('isTailscaleIp and deriveTsQuicUrls filter and deduplicate properly', () {
    expect(isTailscaleIp('100.64.0.1'), isTrue);
    expect(isTailscaleIp('100.127.255.254'), isTrue);
    expect(isTailscaleIp('100.63.255.255'), isFalse);
    expect(isTailscaleIp('100.128.0.1'), isFalse);
    expect(isTailscaleIp('192.168.1.5'), isFalse);
    expect(isTailscaleIp('::1'), isFalse);

    final urls = deriveTsQuicUrls([
      'hquic://192.168.1.5:6789',
      'hquic://100.64.0.9:6789',
      'hquic://relay.example.com:443?relay=AAAA',
      'ws://100.64.0.9:6789',
      'ws://100.100.1.2:6789',
      'hquic://100.64.0.9:6789?punched=1',
      'wss://tun.example/posse',
    ]);
    expect(urls, ['tsquic://100.64.0.9:6789', 'tsquic://100.100.1.2:6789']);

    // quicUrlsOf includes explicit tsHost if not already in hosts
    final parsed = quicUrlsOf({
      'port': 6789,
      'hosts': ['192.168.1.5'],
      'tsHost': '100.64.0.12',
    });
    expect(parsed, ['hquic://192.168.1.5:6789', 'hquic://100.64.0.12:6789']);

    final zh = lookupAppLocalizations(const Locale('zh'));
    expect(hostOf(zh, 'tsquic://100.64.0.9:6789'), '100.64.0.9:6789 · Tailscale');
  });

  test('PosseLink derives tsquic:// only when running, skips headStart and learnQuic upgrade', () async {
    final hostKeys = generateKeyPair();
    final relayToken = base64.encode(Uint8List(16));
    final fakeHost = _FakeHost(
      hostKeys,
      quicInfo: {
        'port': 6789,
        'hosts': ['100.64.0.9'],
        'relay': '1.2.3.4:443',
        'relayToken': relayToken,
      },
    );
    final offer = PairingOffer(
      endpoint: 'ws://100.64.0.9:6789',
      endpoints: const ['ws://100.64.0.9:6789'],
      deviceToken: 'tok',
      publicKeyB64: b64(hostKeys.publicKey),
    );

    // 1) 未运行内置 Tailscale 时不得派生 tsquic://
    final triedWhenOff = <String>[];
    final linkOff = PosseLink(
      offer: offer,
      tailscaleRunning: () => false,
      connect: (url, cancel) async {
        triedWhenOff.add(url);
        return fakeHost.accept();
      },
    );
    await linkOff.start();
    expect(triedWhenOff.any(isTsQuicUrl), isFalse);
    await linkOff.close();

    // 2) 运行时派生 tsquic://，免 quicHeadStart 立刻胜出，且不触发 _learnQuic 切走到中转 QUIC
    final triedWhenOn = <String>[];
    final linkOn = PosseLink(
      offer: offer,
      tailscaleRunning: () => true,
      tailscalePeerRoute: (_) async =>
          const TailscalePeerRoute(kind: 'direct', addr: '1.2.3.4:41641'),
      connect: (url, cancel) async {
        triedWhenOn.add(url);
        if (isTsQuicUrl(url)) return fakeHost.accept();
        throw StateError('non-ts blocked');
      },
    )..quicHeadStart = const Duration(seconds: 5);

    final started = DateTime.now();
    await linkOn.start();
    expect(linkOn.url, 'tsquic://100.64.0.9:6789');
    expect(DateTime.now().difference(started), lessThan(const Duration(seconds: 2)));
    // 等 _learnQuic 跑完，确认它缓存了 quicUrls 但没有把 tsquic:// 换成中转 QUIC
    await Future<void>.delayed(const Duration(milliseconds: 100));
    expect(linkOn.url, 'tsquic://100.64.0.9:6789');
    expect(linkOn.quicUrls.any(isRelayQuicUrl), isTrue);
    expect(linkOn.peerRoute?.kind, 'direct');
    await linkOn.close();
  });

  test('PosseLink prefers self-hosted relay over DERP tsquic://, and falls back to DERP if relay fails', () async {
    final hostKeys = generateKeyPair();
    final fakeHost = _FakeHost(hostKeys);
    final relayUrl = 'hquic://1.2.3.4:443?relay=${base64Url.encode(Uint8List(16))}';
    final offer = PairingOffer(
      endpoint: 'ws://100.64.0.9:6789',
      endpoints: const ['ws://100.64.0.9:6789'],
      deviceToken: 'tok',
      publicKeyB64: b64(hostKeys.publicKey),
    );

    // 1) 对端走 DERP 且中转可用 -> 中转先胜出
    final linkRelay = PosseLink(
      offer: offer,
      tailscaleRunning: () => true,
      tailscalePeerRoute: (_) async =>
          const TailscalePeerRoute(kind: 'derp', relay: 'hkg'),
      connect: (url, cancel) async {
        if (isRelayQuicUrl(url)) {
          await Future<void>.delayed(const Duration(milliseconds: 20));
          return fakeHost.accept();
        }
        if (isTsQuicUrl(url)) return fakeHost.accept();
        throw StateError('ws disabled');
      },
    )
      ..quicUrls = [relayUrl]
      ..tsDerpDelay = const Duration(milliseconds: 200);
    await linkRelay.start();
    expect(isRelayQuicUrl(linkRelay.url!), isTrue);
    await linkRelay.close();

    // 2) 中转失败时 DERP tsquic:// 仍作为兜底连上
    final linkFallback = PosseLink(
      offer: offer,
      tailscaleRunning: () => true,
      tailscalePeerRoute: (_) async =>
          const TailscalePeerRoute(kind: 'derp', relay: 'hkg'),
      connect: (url, cancel) async {
        if (isTsQuicUrl(url)) return fakeHost.accept();
        throw StateError('relay down');
      },
    )
      ..quicUrls = [relayUrl]
      ..tsDerpDelay = const Duration(milliseconds: 40);
    await linkFallback.start();
    expect(linkFallback.url, 'tsquic://100.64.0.9:6789');
    await linkFallback.close();
  });

  test('PosseLink probes instead of tearing down tsquic:// on network signature change', () async {
    final hostKeys = generateKeyPair();
    final fakeHost = _FakeHost(hostKeys);
    final networks = StreamController<String>();
    var wakeCalls = 0;
    var connectCalls = 0;
    final offer = PairingOffer(
      endpoint: 'ws://100.64.0.9:6789',
      endpoints: const ['ws://100.64.0.9:6789'],
      deviceToken: 'tok',
      publicKeyB64: b64(hostKeys.publicKey),
    );
    final link = PosseLink(
      offer: offer,
      networkSignatures: networks.stream,
      tailscaleRunning: () => true,
      onTailscaleWake: () => wakeCalls++,
      connect: (url, cancel) async {
        if (isTsQuicUrl(url)) {
          connectCalls++;
          return fakeHost.accept();
        }
        throw StateError('only tsquic');
      },
    )..wakeProbe = const Duration(milliseconds: 200);

    await link.start();
    expect(link.url, 'tsquic://100.64.0.9:6789');
    expect(connectCalls, 1);

    // 首次签名不触发
    networks.add('wifi');
    await Future<void>.delayed(const Duration(milliseconds: 30));
    expect(connectCalls, 1);

    // 切到蜂窝且原连接仍可应答 ping：只调用 onTailscaleWake + probe，不断开重连
    networks.add('mobile');
    await Future<void>.delayed(const Duration(milliseconds: 80));
    expect(wakeCalls, greaterThanOrEqualTo(1));
    expect(connectCalls, 1);

    // 再次切网但模拟原连接死掉（不应答 ping）：probe 超时后触发断开重连
    fakeHost.answerPing = false;
    networks.add('wifi');
    await Future<void>.delayed(const Duration(milliseconds: 350));
    expect(link.rpc, isNull);

    await link.close();
    await networks.close();
  });

  test('AppSettings and TailscaleController lazy-start and never persist authKey', () async {
    final s = AppSettings(
      tailscaleEnabled: true,
      tailscaleHostname: 'my-iphone',
      tailscaleControlUrl: 'https://hs.example.com',
      tailscaleDisableBind: true,
    );
    final encoded = jsonEncode(s.toJson());
    expect(encoded.contains('authKey'), isFalse);
    final decoded = AppSettings.decode(encoded);
    expect(decoded.tailscaleEnabled, isTrue);
    expect(decoded.tailscaleHostname, 'my-iphone');
    expect(decoded.tailscaleControlUrl, 'https://hs.example.com');
    expect(decoded.tailscaleDisableBind, isTrue);

    // 默认关闭时 restore() 不启动内置 Tailscale（零开销）
    final backend = _FakeTailscaleBackend();
    final modelOff = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
      connect: (_, _) async => throw StateError('unused'),
      tailscaleBackend: backend,
    );
    await modelOff.restore();
    expect(backend.startCalls, 0);
    modelOff.dispose();

    // 开启时 restore() 后台懒启动
    final settingsStore = MemorySettingsStore()..data = encoded;
    final modelOn = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: settingsStore,
      connect: (_, _) async => throw StateError('unused'),
      tailscaleBackend: backend,
    );
    await modelOn.restore();
    await Future<void>.delayed(const Duration(milliseconds: 20));
    expect(backend.startCalls, 1);
    expect(modelOn.tailscale.isRunning, isTrue);
    await modelOn.wake();
    expect(backend.wakeCalls, 1);
    modelOn.dispose();
  });

  test('Desk explicit Tailscale target resolution and direct host bypass for tsquic://', () async {
    // 未运行内置 TS 时不返回 TS 目标
    expect(
      resolveDeskTailscaleTarget(
        readyFrame: const {'port': 40000, 'tsHost': '100.64.0.9', 'tsPort': 40001},
        connectedUrl: 'tsquic://100.64.0.9:6789',
        otherUrls: const [],
        tailscaleRunning: false,
      ),
      isNull,
    );

    // Mac 内置 TS 在 ready 中给出 tsHost + tsPort 时优先使用
    expect(
      resolveDeskTailscaleTarget(
        readyFrame: const {'port': 40000, 'tsHost': '100.64.0.9', 'tsPort': 40001},
        connectedUrl: 'wss://tun.example/posse',
        otherUrls: const ['ws://100.64.0.88:6789'],
        tailscaleRunning: true,
      ),
      const DeskTailscaleTarget('100.64.0.9', 40001),
    );

    // Mac 走官方 Tailscale（ready 无 tsHost）时回退到当前 tsquic:// 主机 + ready.port
    expect(
      resolveDeskTailscaleTarget(
        readyFrame: const {'port': 40000},
        connectedUrl: 'tsquic://100.64.0.9:6789',
        otherUrls: const [],
        tailscaleRunning: true,
      ),
      const DeskTailscaleTarget('100.64.0.9', 40000),
    );

    // deskDirectHostForUrl 对 tsquic:// 跳过 100.x（避免原生 OS UDP 发往不可达的 CGNAT IP），但保留局域网地址
    expect(
      await deskDirectHostForUrl(
        'tsquic://100.64.0.9:6789',
        const ['100.64.0.9', '100.64.0.10'],
      ),
      isNull,
    );
    expect(
      await deskDirectHostForUrl(
        'tsquic://100.64.0.9:6789',
        const ['100.64.0.9', '192.168.1.20'],
      ),
      '192.168.1.20',
    );
    // 非 tsquic://（如官方 Tailscale hquic://）保持原样直连
    expect(
      await deskDirectHostForUrl(
        'hquic://100.64.0.9:6789',
        const ['192.168.1.20'],
      ),
      '100.64.0.9',
    );

    final zh = lookupAppLocalizations(const Locale('zh'));
    expect(deskPathLabel(zh, 'tailscale'), 'Tailscale');

    // DNS 名称仅解析到 100.64/10 时必须被跳过；混合解析（100.64 + 局域网）应返回已验证的非 TS IPv4 字面量
    expect(
      await deskDirectHostForUrl(
        'tsquic://100.64.0.9:6789',
        const ['mac.tail.ts.net'],
        lookup: (_) async => [InternetAddress('100.64.0.10')],
      ),
      isNull,
    );
    expect(
      await deskDirectHostForUrl(
        'tsquic://100.64.0.9:6789',
        const ['mac.local'],
        lookup: (_) async => [
          InternetAddress('100.64.0.10'),
          InternetAddress('192.168.1.25'),
        ],
      ),
      '192.168.1.25',
    );
  });

  test('cold start retries immediately with tsquic:// when Tailscale becomes Running mid-dial', () async {
    final hostKeys = generateKeyPair();
    final fakeHost = _FakeHost(hostKeys);
    final payload = {
      'v': 2,
      'endpoint': 'ws://100.64.0.9:6789',
      'endpoints': ['ws://100.64.0.9:6789'],
      'deviceToken': 'tok',
      'publicKeyB64': b64(hostKeys.publicKey),
    };
    final code = base64Url.encode(utf8.encode(jsonEncode(payload))).replaceAll('=', '');

    final backend = _FakeTailscaleBackend();
    final gate = Completer<String>();
    backend.stateDirGate = gate;

    final saved = SavedComputers(
      computers: [SavedComputer(id: 'mac@ws', code: code)],
      active: 'mac@ws',
    );
    final settings = AppSettings(tailscaleEnabled: true);
    final dialed = <String>[];
    final model = PosseAppModel(
      store: MemoryPairingStore()..code = saved.encode(),
      settingsStore: MemorySettingsStore()..data = jsonEncode(settings.toJson()),
      tailscaleBackend: backend,
      connect: (url, cancel) async {
        dialed.add(url);
        if (isTsQuicUrl(url)) return fakeHost.accept();
        // 模拟普通 ws:// 长时间挂住，等取消后抛异常
        final never = Completer<ByteSocket>();
        Timer.periodic(const Duration(milliseconds: 10), (t) {
          if (cancel.cancelled && !never.isCompleted) {
            t.cancel();
            never.completeError(const RaceCancelled());
          }
        });
        return never.future;
      },
    );

    final restoring = model.restore();
    await Future<void>.delayed(const Duration(milliseconds: 40));
    expect(dialed, ['ws://100.64.0.9:6789']);

    // 拨号途中内置 Tailscale 完成启动变为 Running -> 立刻取消挂起的 ws 并改用 tsquic:// 连上
    gate.complete('/tmp/fake-ts');
    await restoring.timeout(const Duration(seconds: 3));
    expect(model.connectedUrl, 'tsquic://100.64.0.9:6789');
    expect(dialed.any(isTsQuicUrl), isTrue);
    model.dispose();
  });

  test('TailscaleController serializes concurrent start/stop and invalidates stale peerRoute after wake', () async {
    final backend = _FakeTailscaleBackend();
    final dirGate = Completer<String>();
    backend.stateDirGate = dirGate;
    final ctrl = TailscaleController(backend: backend);

    final fStart = ctrl.start(hostname: 'phone');
    expect(ctrl.busy, isTrue);
    final fStop = ctrl.stop();
    expect(ctrl.busy, isTrue);

    // start 等完 stateDir 后发现已被 stop 取代，绝不能倒序把节点重新打开
    dirGate.complete('/tmp/fake-ts');
    await Future.wait([fStart, fStop]);
    expect(ctrl.busy, isFalse);
    expect(ctrl.isRunning, isFalse);
    expect(backend.ops, ['stop']);

    // 启动并验证 wake() 后在途旧 DERP 查询不污染新缓存
    backend.stateDirGate = null;
    await ctrl.start(hostname: 'phone');
    expect(ctrl.isRunning, isTrue);

    final firstRouteGate = Completer<TailscalePeerRoute>();
    var routeCalls = 0;
    backend.peerRouteHandler = (_) {
      routeCalls++;
      if (routeCalls == 1) return firstRouteGate.future;
      return Future.value(const TailscalePeerRoute(kind: 'direct', addr: '1.2.3.4:41641'));
    };

    final staleQuery = ctrl.peerRoute('100.64.0.9');
    ctrl.wake();
    firstRouteGate.complete(const TailscalePeerRoute(kind: 'derp', relay: 'hkg'));
    // 跨 wake 的旧查询本身也不能把旧 DERP 交回调用方，应自动重查拿到最新的 direct
    final staleResult = await staleQuery;
    expect(staleResult.kind, 'direct');

    final fresh = await ctrl.peerRoute('100.64.0.9');
    expect(fresh.kind, 'direct');
    ctrl.dispose();
  });

  test('first pairing preserves retry window, disconnect blocks TS reconnect, and cancelled handshake closes socket', () async {
    final hostKeys = generateKeyPair();
    final fakeHost = _FakeHost(hostKeys);

    // 1) 仅有局域网地址的首次配对：Tailscale 变为 Running 不打断且保留权限重试窗口
    final lanPayload = {
      'v': 2,
      'endpoint': 'ws://192.168.1.9:6789',
      'endpoints': ['ws://192.168.1.9:6789'],
      'deviceToken': 'tok',
      'publicKeyB64': b64(hostKeys.publicKey),
    };
    final lanCode = base64Url.encode(utf8.encode(jsonEncode(lanPayload))).replaceAll('=', '');
    final backend = _FakeTailscaleBackend();
    var lanTries = 0;
    final firstModel = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
      tailscaleBackend: backend,
      connect: (url, cancel) async {
        lanTries++;
        if (lanTries == 1) {
          backend.emit(const TailscaleStatus(enabled: true, state: 'running', ip: '100.64.0.50'));
          throw const SocketException('Local network permission pending');
        }
        return fakeHost.accept();
      },
    )
      ..firstConnectRetry = const Duration(milliseconds: 20)
      ..firstConnectWindow = const Duration(seconds: 2);
    expect(await firstModel.connectCode(lanCode), isTrue);
    expect(lanTries, 2);

    // 2) 主动断开在等待旧连接关闭的间隙遇到 Tailscale Running 绝不自动重连
    final closeGate = Completer<void>();
    final slowModel = PosseAppModel(
      store: MemoryPairingStore()
        ..code = SavedComputers(
          computers: [SavedComputer(id: 'mac@ws', code: lanCode)],
          active: 'mac@ws',
        ).encode(),
      settingsStore: MemorySettingsStore(),
      tailscaleBackend: backend,
      connect: (url, cancel) async {
        final inner = fakeHost.accept();
        return _SlowCloseSocket(inner, closeGate.future);
      },
    );
    expect(await slowModel.connectCode(lanCode), isTrue);
    backend.emit(const TailscaleStatus(enabled: false, state: 'disabled'));
    final disconnecting = slowModel.disconnect();
    await Future<void>.delayed(const Duration(milliseconds: 20));
    backend.emit(const TailscaleStatus(enabled: true, state: 'running', ip: '100.64.0.50'));
    await Future<void>.delayed(const Duration(milliseconds: 30));
    closeGate.complete();
    await disconnecting;
    expect(slowModel.disconnected, isTrue);
    expect(slowModel.rpc, isNull);
    firstModel.dispose();
    slowModel.dispose();

    // 3) 握手等待 ready 期间取消单轮竞速必须立刻关闭候选 socket
    var socketClosed = false;
    final tsOffer = PairingOffer(
      endpoint: 'ws://100.64.0.9:6789',
      endpoints: const ['ws://100.64.0.9:6789'],
      deviceToken: 'tok',
      publicKeyB64: b64(hostKeys.publicKey),
    );
    final link = PosseLink(
      offer: tsOffer,
      connect: (url, cancel) async {
        final (client, _) = pipePair();
        return _TrackCloseSocket(client, () => socketClosed = true);
      },
    );
    final opening = link.open();
    await Future<void>.delayed(const Duration(milliseconds: 20));
    link.cancelPendingOpen();
    await expectLater(opening, throwsA(isA<RaceCancelled>()));
    await Future<void>.delayed(const Duration(milliseconds: 20));
    expect(socketClosed, isTrue);
    await link.close();
  });

  test('PosseLink upgrades WebSocket to tsquic:// when posse.quic learns tsHost, and rejects foreign tsquic:// pairing codes', () async {
    final hostKeys = generateKeyPair();
    final fakeHost = _FakeHost(
      hostKeys,
      quicInfo: {
        'port': 6789,
        'tsHost': '100.64.0.77',
      },
    );
    final offer = PairingOffer(
      endpoint: 'wss://tun.example/posse',
      endpoints: const ['wss://tun.example/posse'],
      deviceToken: 'tok',
      publicKeyB64: b64(hostKeys.publicKey),
    );
    final link = PosseLink(
      offer: offer,
      tailscaleRunning: () => true,
      connect: (url, cancel) async {
        if (url == 'wss://tun.example/posse' || url == 'tsquic://100.64.0.77:6789') {
          return fakeHost.accept();
        }
        throw StateError('blocked $url');
      },
    );
    await link.start();
    // _learnQuic 发现 tsHost 后应自动从 wss:// 升级到 tsquic://
    await Future<void>.delayed(const Duration(milliseconds: 80));
    expect(link.url, 'tsquic://100.64.0.77:6789');
    // 持久化给调用方的 quicUrls 不含 tsquic://
    expect(link.quicUrls.any(isTsQuicUrl), isFalse);
    await link.close();

    // 外部配对码无论主地址还是附加地址夹带 tsquic:// 都必须被 PairingOffer.parse 拒绝
    final badPayload = {
      'v': 2,
      'endpoint': 'tsquic://100.64.0.9:6789',
      'endpoints': ['tsquic://100.64.0.9:6789'],
      'deviceToken': 'tok',
      'publicKeyB64': b64(hostKeys.publicKey),
    };
    final badCode = base64Url.encode(utf8.encode(jsonEncode(badPayload))).replaceAll('=', '');
    expect(() => PairingOffer.parse(badCode), throwsFormatException);

    final badExtraPayload = {
      'v': 2,
      'endpoint': 'ws://192.168.1.9:6789',
      'endpoints': ['ws://192.168.1.9:6789', 'tsquic://100.64.0.9:6789'],
      'deviceToken': 'tok',
      'publicKeyB64': b64(hostKeys.publicKey),
    };
    final badExtraCode = base64Url.encode(utf8.encode(jsonEncode(badExtraPayload))).replaceAll('=', '');
    expect(() => PairingOffer.parse(badExtraCode), throwsFormatException);
  });
}

class _SlowCloseSocket implements ByteSocket {
  _SlowCloseSocket(this._inner, this._gate);
  final ByteSocket _inner;
  final Future<void> _gate;
  @override
  Stream<SocketEvent> get events => _inner.events;
  @override
  Future<void> sendText(String text) => _inner.sendText(text);
  @override
  Future<void> sendBinary(Uint8List bytes) => _inner.sendBinary(bytes);
  @override
  int? get closeCode => _inner.closeCode;
  @override
  Future<void> close() async {
    await _gate;
    await _inner.close();
  }
}

class _TrackCloseSocket implements ByteSocket {
  _TrackCloseSocket(this._inner, this._onClose);
  final ByteSocket _inner;
  final void Function() _onClose;
  @override
  Stream<SocketEvent> get events => _inner.events;
  @override
  Future<void> sendText(String text) => _inner.sendText(text);
  @override
  Future<void> sendBinary(Uint8List bytes) => _inner.sendBinary(bytes);
  @override
  int? get closeCode => _inner.closeCode;
  @override
  Future<void> close() async {
    _onClose();
    await _inner.close();
  }
}
