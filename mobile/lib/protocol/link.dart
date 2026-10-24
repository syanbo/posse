import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

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

/// 和一台桌面握手：hello、ready、auth，然后声明 Hive 能力。
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
    ).asPhone();
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
      Uint8List.fromList(utf8.encode(auth)),
      direction: dirMobileToDesktop,
      kind: kindText,
    );
    final authEvent = await reader.next(cancel);
    if (authEvent.binary) throw E2eeException('auth reply was binary');
    final plain = session.openText(
      authEvent.bytes,
      direction: dirDesktopToMobile,
    );
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
class HiveLink {
  HiveLink({
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

  /// 电脑经 hive.quic 给的 QUIC 地址。连上 WebSocket 之后才知道，之后的重连先试它们。
  List<String> quicUrls = const [];

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
      if (!isQuicUrl(won.url)) unawaited(_upgrade(won.value, epoch));
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
          throw TimeoutException('connect $url');
        },
      );

  /// 换上新连接，关掉原来的。
  Future<void> _adopt(RaceResult<PhoneRpc> won, int epoch) async {
    // 套接字自己断了再握手。主动 close 不会进这里。先把死掉的连接放掉，界面才知道断了。
    won.value.watchClose(() {
      if (epoch != _epoch || !identical(rpc, won.value)) return;
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
    await previous?.close();
  }

  /// 走 WebSocket 连上之后问电脑要 QUIC 地址，连得上就换过去。连不上什么都不变。
  Future<void> _upgrade(PhoneRpc via, int epoch) async {
    final List<String> urls;
    try {
      urls = quicUrlsOf(await via.call('hive.quic', const {}, timeout: const Duration(seconds: 5)));
    } catch (_) {
      // Orca 和旧版 Hive 没有这个方法
      return;
    }
    quicUrls = urls;
    if (urls.isEmpty || _closed || epoch != _epoch) return;
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
    await _adopt(won, ++_epoch);
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

  Future<void> _reopenNow() async {
    try {
      await open();
    } catch (_) {}
  }

  var _closed = false;
  var _reopening = false;

  /// 断线后一直重连，间隔从 0.3 秒翻倍到 5 秒，连上或者 [close] 为止。电脑重启常驻进程时手机自己接上。
  Future<void> _reopen() async {
    if (_reopening) return;
    _reopening = true;
    var delay = const Duration(milliseconds: 300);
    try {
      while (!_closed && rpc == null) {
        await Future<void>.delayed(delay);
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
