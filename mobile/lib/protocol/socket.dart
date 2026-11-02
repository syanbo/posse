import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:web_socket_channel/web_socket_channel.dart';

import 'e2ee.dart';
import 'race.dart';

class SocketEvent {
  SocketEvent.text(List<int> bytes)
    : binary = false,
      bytes = Uint8List.fromList(bytes);
  SocketEvent.binary(List<int> bytes)
    : binary = true,
      bytes = Uint8List.fromList(bytes);

  final bool binary;
  final Uint8List bytes;
}

abstract class ByteSocket {
  Future<void> sendText(String text);
  Future<void> sendBinary(Uint8List bytes);
  Stream<SocketEvent> get events;
  Future<void> close();

  /// 对方关连接时给的关闭码，[events] 结束后读；不知道时是 null。
  int? get closeCode;
}

class WebSocketByteSocket implements ByteSocket {
  WebSocketByteSocket(this._channel);

  final WebSocketChannel _channel;

  static Future<WebSocketByteSocket> connect(
    String url,
    CancelToken cancel,
  ) async {
    if (cancel.cancelled) throw const RaceCancelled();
    final channel = WebSocketChannel.connect(Uri.parse(url));
    final socket = WebSocketByteSocket(channel);
    if (cancel.cancelled) {
      await socket.close();
      throw const RaceCancelled();
    }
    try {
      await channel.ready;
    } catch (error) {
      // 没连上的通道关起来可能永远不结束，不能等它，否则这次尝试一直挂着、重连也停在这里
      unawaited(socket.close().catchError((Object _) {}));
      rethrow;
    }
    if (cancel.cancelled) {
      await socket.close();
      throw const RaceCancelled();
    }
    return socket;
  }

  @override
  late final Stream<SocketEvent> events = _channel.stream.map((event) {
    if (event is String) return SocketEvent.text(utf8.encode(event));
    if (event is List<int>) return SocketEvent.binary(event);
    throw StateError('unexpected websocket event ${event.runtimeType}');
  });

  @override
  Future<void> sendText(String text) async => _channel.sink.add(text);

  @override
  Future<void> sendBinary(Uint8List bytes) async => _channel.sink.add(bytes);

  @override
  Future<void> close() async {
    await _channel.sink.close();
  }

  @override
  int? get closeCode => _channel.closeCode;
}

/// 测试里把两端接在一起。一边 send，另一边的 events 收到。
class PipeSocket implements ByteSocket {
  PipeSocket() : _incoming = StreamController<SocketEvent>();

  final StreamController<SocketEvent> _incoming;
  PipeSocket? peer;
  var _closed = false;

  @override
  Stream<SocketEvent> get events => _incoming.stream;

  @override
  Future<void> sendText(String text) async {
    _deliver(SocketEvent.text(utf8.encode(text)));
  }

  @override
  Future<void> sendBinary(Uint8List bytes) async {
    _deliver(SocketEvent.binary(bytes));
  }

  void _deliver(SocketEvent event) {
    final other = peer;
    if (_closed || other == null || other._closed || other._incoming.isClosed) {
      return;
    }
    other._incoming.add(event);
  }

  @override
  Future<void> close([int? code]) async {
    _closed = true;
    if (!_incoming.isClosed) await _incoming.close();
    // 和真的 WebSocket 一样，一头挂断另一头也收到结束和关闭码
    final other = peer;
    if (other != null && !other._incoming.isClosed) {
      other.closeCode = code;
      await other._incoming.close();
    }
  }

  @override
  int? closeCode;
}

(PipeSocket, PipeSocket) pipePair() {
  final a = PipeSocket();
  final b = PipeSocket();
  a.peer = b;
  b.peer = a;
  return (a, b);
}

/// 一条连接上只有一个读者，握手和之后的 RPC 共用它，避免二次 listen 丢掉帧。
class SocketReader {
  SocketReader(Stream<SocketEvent> events) {
    _sub = events.listen(
      (event) {
        if (_waiters.isNotEmpty) {
          _waiters.removeAt(0).complete(event);
        } else {
          _buffer.add(event);
        }
      },
      onError: (Object error) {
        _fail(error);
      },
      onDone: () => _fail(E2eeException('socket closed')),
    );
  }

  final _buffer = <SocketEvent>[];
  final _waiters = <Completer<SocketEvent>>[];
  late final StreamSubscription<SocketEvent> _sub;
  var _done = false;

  Future<SocketEvent> next(CancelToken cancel) {
    if (cancel.cancelled) return Future.error(const RaceCancelled());
    if (_buffer.isNotEmpty) return Future.value(_buffer.removeAt(0));
    if (_done) return Future.error(E2eeException('socket closed'));
    final waiter = Completer<SocketEvent>();
    _waiters.add(waiter);
    return waiter.future;
  }

  void _fail(Object error) {
    _done = true;
    final pending = List<Completer<SocketEvent>>.of(_waiters);
    _waiters.clear();
    for (final waiter in pending) {
      if (!waiter.isCompleted) waiter.completeError(error);
    }
  }

  Future<void> cancel() => _sub.cancel();
}
