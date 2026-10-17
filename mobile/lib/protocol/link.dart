import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:connectivity_plus/connectivity_plus.dart';

import 'e2ee.dart';
import 'pairing.dart';
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
    await rpc.declareCapabilities();
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
  }) : networkSignatures = networkSignatures ?? const Stream.empty();

  final PairingOffer offer;
  final SocketConnector connect;
  final Stream<String> networkSignatures;

  PhoneRpc? rpc;
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

  Future<PhoneRpc> open() async {
    final epoch = ++_epoch;
    try {
      final won = await raceEndpoints<PhoneRpc>(
        urls: offer.dialUrls,
        attempt: (url, cancel) async {
          final socket = await connect(url, cancel);
          if (cancel.cancelled) {
            await socket.close();
            throw const RaceCancelled();
          }
          try {
            return await handshake(
              socket: socket,
              offer: offer,
              cancel: cancel,
            );
          } catch (error) {
            await socket.close();
            rethrow;
          }
        },
        abandon: (value) => value.close(),
      );
      if (epoch != _epoch) {
        await won.value.close();
        throw const RaceCancelled();
      }
      // 套接字自己断了再握手。主动 close 不会进这里。
      won.value.watchClose(() {
        if (epoch != _epoch || !identical(rpc, won.value)) return;
        unawaited(_reopen());
      });
      final previous = rpc;
      rpc = won.value;
      lastError = null;
      if (!_ready.isClosed) _ready.add(won.value);
      onActivity?.call();
      await previous?.close();
      return won.value;
    } catch (error) {
      if (epoch == _epoch) {
        lastError = error;
        onActivity?.call();
      }
      rethrow;
    }
  }

  Future<void> _reopenNow() async {
    try {
      await open();
    } catch (_) {}
  }

  Future<void> _reopen() async {
    await Future<void>.delayed(const Duration(milliseconds: 300));
    await _reopenNow();
  }

  Future<void> close() async {
    _epoch++;
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
