import 'dart:async';
import 'dart:convert';

import 'package:connectivity_plus/connectivity_plus.dart';

import 'e2ee.dart';
import 'latency.dart';
import 'pairing.dart';
import 'quic_socket.dart';
import 'race.dart';
import 'rpc.dart';
import 'socket.dart';

typedef SocketConnector = Future<ByteSocket> Function(
  String url,
  CancelToken cancel,
);

/// 打洞：经 [relayUrl] 的中转问出手机这边的公网地址，交给 [exchange]（经现有连接问电脑）换回电脑的
/// 公网地址，向它打洞拨号。返回连上的套接字和电脑的地址。
typedef QuicPuncher = Future<(ByteSocket, String)> Function(
  String relayUrl,
  Future<String> Function(String mine) exchange,
);

/// 和一台桌面握手：hello、ready、auth，然后声明 Posse 能力。
Future<PhoneRpc> handshake({
  required ByteSocket socket,
  required PairingOffer offer,
  required CancelToken cancel,
  Map<String, Object?>? phone,
}) async {
  final keys = generateKeyPair();
  final clientNonce = randomBytes(32);
  final hello = Hello(
    clientPublicKey: keys.publicKey,
    clientNonce: clientNonce,
    context: const HelloContext(
      protocol: protocolName,
      initiator: 'mobile',
      responder: 'desktop',
      transport: 'direct',
    ),
  );
  // 先开始读，再发 hello，免得 ready 在 listen 之前到达被丢掉。
  final reader = SocketReader(socket.events);
  try {
    await socket.sendText(jsonEncode(hello.toJson()));
    final readyEvent = await reader.next(cancel);
    if (readyEvent.binary) throw E2eeException('ready was binary');
    final ready = parseReady(utf8.decode(readyEvent.bytes), hello);
    if (ready == null) throw E2eeException('bad ready');
    if (b64(ready.desktopPublicKey) != offer.publicKeyB64) {
      throw E2eeException('host key does not match the pairing offer');
    }
    if (cancel.cancelled) throw const RaceCancelled();

    final transcript = encodeTranscript(hello, ready);
    final session = deriveSession(
      shared: beforeNm(ready.desktopPublicKey, keys.secretKey),
      transcript: transcript,
      clientNonce: clientNonce,
      desktopNonce: ready.desktopNonce,
    ).asPhone()..framing = ready.framing;
    final rpc = PhoneRpc(
      socket: socket,
      session: session,
      deviceToken: offer.deviceToken,
      reader: reader,
    );
    final auth = jsonEncode({
      'type': 'e2ee_auth',
      'v': 2,
      'transcriptHashB64': b64(session.transcriptHash),
      'deviceToken': offer.deviceToken,
    });
    await rpc.sendPlain(
      utf8.encode(auth),
      direction: dirMobileToDesktop,
      kind: kindText,
    );
    final authEvent = await reader.next(cancel);
    final (kind, plain) = session.openMessage(
      authEvent.bytes,
      binary: authEvent.binary,
      direction: dirDesktopToMobile,
    );
    if (kind != kindText) throw E2eeException('auth reply was not a message');
    final reply = jsonDecode(utf8.decode(plain));
    if (reply is! Map || reply['type'] != 'e2ee_authenticated') {
      throw E2eeException('authentication rejected');
    }
    rpc.start();
    await rpc.declareCapabilities(phone: phone);
    return rpc;
  } catch (error) {
    await reader.cancel();
    rethrow;
  }
}

/// 多地址并发握手。网络签名变化时关掉当前连接再跑一遍。
class PosseLink {
  PosseLink({
    required this.offer,
    required this.connect,
    Stream<String>? networkSignatures,
    this.attemptTimeout = const Duration(seconds: 10),
    this.phone,
  }) : networkSignatures = networkSignatures ?? const Stream.empty();

  /// 每次尝试（连上加握手）最多等多久。

  final PairingOffer offer;
  final SocketConnector connect;
  final Stream<String> networkSignatures;
  final Duration attemptTimeout;

  /// 每次握手时报给电脑的手机身份，见 [PhoneRpc.declareCapabilities]。
  final Map<String, Object?>? Function()? phone;

  PhoneRpc? rpc;

  /// 当前连接的延迟，见 [LatencyMeter]；没连上时为空。
  LatencyMeter? meter;

  /// [meter] 的读数变了。
  void Function()? onLatency;

  /// 测延迟的间隔和每次等多久，测试里改小。
  Duration latencyInterval = const Duration(seconds: 5);
  Duration latencyTimeout = const Duration(seconds: 5);

  /// 这次连上的是配对码里的哪个地址（几个地址同时试，先握手成功的那个）。
  String? url;
  Object? lastError;
  void Function()? onActivity;
  var _epoch = 0;
  StreamSubscription<String>? _network;
  final _gate = NetworkGate();
  final _ready = StreamController<PhoneRpc>.broadcast();

  Stream<PhoneRpc> get connections => _ready.stream;

  Future<PhoneRpc> start() async {
    final rpc = await open();
    // 第一次握手完成再听网络。听得太早时，签名一变就会另开一轮，
    // 把这次握手的 epoch 顶掉，调用方只看到 RaceCancelled。
    _network ??= networkSignatures.listen((signature) {
      if (_gate.shouldReconnect(signature)) unawaited(_reopenNow());
    });
    return rpc;
  }

  /// 电脑经 posse.quic 给的 QUIC 地址，重连先试它们。调用方可以先填上次存的，冷启动不用先连 WebSocket。
  List<String> quicUrls = const [];

  /// 电脑给了新的 QUIC 地址（每次连上都问一次），调用方存下来。
  void Function(List<String> urls)? onQuicUrls;

  /// 经中转连上之后打洞直连，见 [QuicPuncher]。为空时不打洞（测试、没有原生库的平台）。
  QuicPuncher? punch;

  /// 知道 QUIC 地址时 WebSocket 晚这么久才开始连：两条都通时留下 QUIC，QUIC 不通时最多慢这么多。
  Duration quicHeadStart = const Duration(seconds: 2);

  Future<PhoneRpc> open() async {
    final epoch = ++_epoch;
    final quic = quicUrls;
    try {
      final won = await raceEndpoints<PhoneRpc>(
        urls: [...quic, ...offer.dialUrls],
        attempt: (url, cancel) async {
          if (quic.isNotEmpty && !isQuicUrl(url)) {
            await Future<void>.delayed(quicHeadStart);
            if (cancel.cancelled) throw const RaceCancelled();
          }
          return _timedAttempt(url, cancel);
        },
        abandon: (value) => value.close(),
      );
      if (epoch != _epoch) {
        await won.value.close();
        throw const RaceCancelled();
      }
      await _adopt(won, epoch);
      unawaited(_learnQuic(won.value, epoch, upgrade: !isQuicUrl(won.url)));
      return won.value;
    } catch (error) {
      if (epoch == _epoch) {
        lastError = error;
        onActivity?.call();
      }
      rethrow;
    }
  }

  // 每次尝试有时限，超时就算失败；不然某个地址一直挂着，断线重连会停在这一轮
  Future<PhoneRpc> _timedAttempt(String url, CancelToken cancel) =>
      _attempt(url, cancel).timeout(
        attemptTimeout,
        onTimeout: () {
          cancel.cancel();
          // 不带整个地址：中转的地址里有令牌，报错可能显示出来
          throw TimeoutException('connect ${Uri.tryParse(url)?.host ?? url}');
        },
      );

  /// 换上新连接，关掉原来的。[drain] 时原来的连接还好好的，等它上面的请求回完再关。
  Future<void> _adopt(
    RaceResult<PhoneRpc> won,
    int epoch, {
    bool drain = false,
  }) async {
    // 套接字自己断了再握手。主动 close 不会进这里。先把死掉的连接放掉，界面才知道断了。
    // 只看它还是不是当前连接，不看 epoch：切网络后另开的一轮会加 epoch，那一轮没连上时
    // 这条还在用，它断了也要放掉，不然界面一直拿着死连接、不再重连。
    won.value.watchClose(() {
      if (_closed || !identical(rpc, won.value)) return;
      rpc = null;
      meter?.stop();
      meter = null;
      onActivity?.call();
      unawaited(_reopen());
    });
    final previous = rpc;
    rpc = won.value;
    url = won.url;
    meter?.stop();
    final latest = LatencyMeter(
      won.value,
      interval: latencyInterval,
      timeout: latencyTimeout,
      onChange: () {
        if (identical(rpc, won.value)) onLatency?.call();
      },
    );
    meter = latest..start();
    lastError = null;
    if (!_ready.isClosed) _ready.add(won.value);
    onActivity?.call();
    if (isRelayQuicUrl(won.url)) unawaited(_punch(won.value, won.url, epoch));
    if (drain) {
      unawaited(previous?.closeWhenIdle());
    } else {
      await previous?.close();
    }
  }

  /// 经中转连着时打洞：手机新开一个 socket 问出公网地址，经 posse.punch 交给电脑、换回电脑的地址，
  /// 两边同时打洞后直接拨 QUIC。打通就换过去，旧的中转连接等订阅迁走再关；打不通什么都不变。
  Future<void> _punch(PhoneRpc via, String relayUrl, int epoch) async {
    final puncher = punch;
    if (puncher == null) return;
    final ByteSocket socket;
    final String peer;
    try {
      (socket, peer) = await puncher(relayUrl, (mine) async {
        final result = await via.call('posse.punch', {
          'addr': mine,
        }, timeout: const Duration(seconds: 5));
        final addr = result['addr'];
        if (addr is! String || addr.isEmpty) throw StateError('no address');
        return addr;
      });
    } catch (_) {
      return;
    }
    if (_closed || epoch != _epoch || !identical(rpc, via)) {
      unawaited(socket.close());
      return;
    }
    final PhoneRpc direct;
    try {
      direct = await handshake(
        socket: socket,
        offer: offer,
        cancel: CancelToken(),
        phone: phone?.call(),
      ).timeout(attemptTimeout);
    } catch (_) {
      unawaited(socket.close().catchError((Object _) {}));
      return;
    }
    if (_closed || epoch != _epoch || !identical(rpc, via)) {
      await direct.close();
      return;
    }
    await _adopt(RaceResult(punchedUrl(peer), direct), ++_epoch, drain: true);
  }

  /// 连上之后问电脑要 QUIC 地址，记下来（[onQuicUrls] 存盘，冷启动直接拨）。[upgrade]（这次走的是
  /// WebSocket）时连得上 QUIC 就换过去，连不上什么都不变。
  Future<void> _learnQuic(
    PhoneRpc via,
    int epoch, {
    required bool upgrade,
  }) async {
    final List<String> urls;
    try {
      urls = quicUrlsOf(
        await via.call(
          'posse.quic',
          const {},
          timeout: const Duration(seconds: 5),
        ),
      );
    } catch (_) {
      // Orca 和旧版 Posse 没有这个方法
      return;
    }
    if (_closed || epoch != _epoch) return;
    quicUrls = urls;
    onQuicUrls?.call(urls);
    if (!upgrade || urls.isEmpty) return;
    final RaceResult<PhoneRpc> won;
    try {
      won = await raceEndpoints<PhoneRpc>(
        urls: urls,
        attempt: _timedAttempt,
        abandon: (value) => value.close(),
      );
    } catch (_) {
      return;
    }
    if (_closed || epoch != _epoch || !identical(rpc, via)) {
      await won.value.close();
      return;
    }
    await _adopt(won, ++_epoch, drain: true);
  }

  Future<PhoneRpc> _attempt(String url, CancelToken cancel) async {
    final socket = await connect(url, cancel);
    if (cancel.cancelled) {
      unawaited(socket.close().catchError((Object _) {}));
      throw const RaceCancelled();
    }
    try {
      return await handshake(
        socket: socket,
        offer: offer,
        cancel: cancel,
        phone: phone?.call(),
      );
    } catch (error) {
      unawaited(socket.close().catchError((Object _) {}));
      rethrow;
    }
  }

  /// 回到前台时等电脑回话最多这么久。往返平时几十到几百毫秒，经中转也不过半秒。
  Duration wakeProbe = const Duration(seconds: 2);

  /// 从后台回到前台：iOS 在后台冻住 App，连接多半已经死了（NAT 映射过期、电脑那边超时），
  /// 等测延迟连着超时三次要十几秒，这期间界面没反应。马上测一次，[wakeProbe] 内没回话就断开重连；
  /// 本来就断着的不等退避，马上连。
  Future<void> wake() async {
    if (_closed) return;
    final current = rpc;
    if (current == null) {
      if (!_reopening || _waiting) unawaited(_wakeOpen ??= _openOnWake());
      return;
    }
    final alive = await (meter?.probe(wakeProbe) ?? Future.value(true));
    if (!alive && !_closed && identical(rpc, current)) current.drop();
  }

  /// [wake] 发起、还没结束的那次连接。[_reopen] 等完它再决定要不要自己连，不另开一轮把它顶掉。
  Future<void>? _wakeOpen;

  Future<void> _openOnWake() async {
    try {
      await open();
    } catch (_) {
    } finally {
      _wakeOpen = null;
    }
  }

  Future<void> _reopenNow() async {
    try {
      await open();
    } catch (_) {}
  }

  var _closed = false;
  var _reopening = false;

  /// [_reopen] 正在两次重试之间等。
  var _waiting = false;

  /// 断线后一直重连，间隔从 0.3 秒翻倍到 5 秒，连上或者 [close] 为止。电脑重启常驻进程时手机自己接上。
  Future<void> _reopen() async {
    if (_reopening) return;
    _reopening = true;
    var delay = const Duration(milliseconds: 300);
    try {
      while (!_closed && rpc == null) {
        _waiting = true;
        await Future<void>.delayed(delay);
        _waiting = false;
        final woken = _wakeOpen;
        if (woken != null) await woken;
        if (_closed || rpc != null) return;
        try {
          await open();
          return;
        } catch (_) {}
        // 局域网里电脑重启常驻进程只要几秒，间隔封顶 5 秒，回来得快
        delay = delay * 2 > const Duration(seconds: 5)
            ? const Duration(seconds: 5)
            : delay * 2;
      }
    } finally {
      _reopening = false;
    }
  }

  Future<void> close() async {
    _closed = true;
    _epoch++;
    meter?.stop();
    meter = null;
    await _network?.cancel();
    _network = null;
    await rpc?.close();
    rpc = null;
    await _ready.close();
  }
}

Stream<String> connectivitySignatures() {
  return Connectivity().onConnectivityChanged.map(
    (results) => results.map((item) => item.name).join(','),
  );
}
