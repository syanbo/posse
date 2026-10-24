import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'e2ee.dart';
import 'race.dart';
import 'socket.dart';

const hiveClientName = 'hive-flutter';
const hiveClientVersion = 1;
const hiveCapabilities = [
  'hive.permission.v1',
  'hive.git.v1',
  'hive.history.v1',
  'hive.status.v1',
  'hive.usage.v1',
  'hive.context.v1',
  'hive.media.v1',
  'hive.desk.v1',
];

class RpcException implements Exception {
  RpcException(this.code, this.message);
  final String code;
  final String message;
  @override
  String toString() => '$code: $message';
}

class RpcStream {
  RpcStream(this.id, this.frames, this.cancel);
  final String id;
  final Stream<Map<String, Object?>> frames;
  final Future<void> Function() cancel;
}

class PhoneRpc {
  // reader 要给其他库传，不能收成私有的 initializing formal。
  PhoneRpc({
    required this.socket,
    required this.session,
    required this.deviceToken,
    required SocketReader reader,
  }) : _reader = reader; // ignore: prefer_initializing_formals

  final ByteSocket socket;
  final E2eeSession session;
  final String deviceToken;
  final SocketReader _reader;

  final _wait = <String, Completer<Map<String, Object?>>>{};
  final _subs = <String, void Function(Map<String, Object?> frame)>{};
  final _subErr = <String, void Function(RpcException error)>{};
  final _binary = StreamController<Uint8List>.broadcast();

  /// 解开后的二进制帧（Orca 终端流，16 字节头还在）。先听再订阅，快照才不会丢。
  Stream<Uint8List> get binary => _binary.stream;
  var _n = 0;
  var _bad = 0;

  /// 收到、发出的帧一共多少字节。测延迟时用来判断这段时间连接上有没有别的大块数据在排队，
  /// 以及连接是不是还活着（一直在收东西就不算没响应）。
  var receivedBytes = 0;
  var sentBytes = 0;

  /// 收到过几个请求的回复（不算订阅推送）。测延迟时用它判断电脑还在不在处理请求。
  var repliesReceived = 0;
  var _closed = false;
  var _ended = false;
  var _unexpectedLoss = false;
  var _deliveredLoss = false;
  var _started = false;
  void Function()? _onClosed;
  Future<void> _tail = Future<void>.value();

  /// 连接自己断掉时调用一次。主动 [close] 不触发，避免关掉之后又重连。
  void watchClose(void Function() callback) {
    _onClosed = callback;
    _deliverLoss();
  }

  void start() {
    if (_started) return;
    _started = true;
    unawaited(_read());
  }

  /// [phone] 是 Hive 扩展：这台手机的安装 ID、型号和存着的这台电脑的全部 token，
  /// 电脑按它把同一台手机的配对合成一项、作废换掉的 token。
  Future<void> declareCapabilities({Map<String, Object?>? phone}) async {
    try {
      await call('runtime.clientCapabilities.update', {
        'client': hiveClientName,
        'version': hiveClientVersion,
        'capabilities': hiveCapabilities,
        'phone': ?phone,
      });
    } on RpcException catch (error) {
      // 旧常驻进程没有这条扩展，照常使用它已经有的方法。
      if (error.code == 'unknown_method' || error.code == 'method_not_found') {
        return;
      }
      rethrow;
    }
  }

  /// [timeout] 默认 20 秒；回复很大、要排在别的大回复后面发的调用（视频分段）给得更长。
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration timeout = const Duration(seconds: 20),
  }) async {
    final id = 'rpc-${++_n}';
    final waiter = Completer<Map<String, Object?>>();
    _wait[id] = waiter;
    try {
      await _sendRequest(id, method, params);
    } catch (error) {
      _wait.remove(id);
      rethrow;
    }
    return waiter.future.timeout(
      timeout,
      onTimeout: () {
        _wait.remove(id);
        throw TimeoutException(method);
      },
    );
  }

  Future<RpcStream> subscribe(
    String method,
    Map<String, Object?> params,
  ) async {
    final id = 'rpc-${++_n}';
    final controller = StreamController<Map<String, Object?>>();
    _subs[id] = (frame) {
      if (!controller.isClosed) controller.add(frame);
    };
    // 订阅失败是一条普通错误回复，不是流里的一帧。
    _subErr[id] = (error) {
      if (!controller.isClosed) controller.addError(error);
    };
    await _sendRequest(id, method, params);
    return RpcStream(id, controller.stream, () async {
      _subs.remove(id);
      _subErr.remove(id);
      if (!controller.isClosed) await controller.close();
    });
  }

  Future<void> sendPlain(
    Uint8List plain, {
    required int direction,
    required int kind,
  }) {
    final result = _tail.then((_) async {
      if (_closed) throw StateError('closed');
      final frame = session.seal(plain, direction: direction, kind: kind);
      sentBytes += frame.length;
      if (kind == kindText) {
        await socket.sendText(base64.encode(frame));
      } else {
        await socket.sendBinary(frame);
      }
    });
    _tail = result.then((_) {}, onError: (_) {});
    return result;
  }

  Future<void> close() async {
    if (_closed) return;
    _closed = true;
    _ended = true;
    _failAll(RpcException('closed', 'connection closed'));
    _closeBinary();
    await _reader.cancel();
    await socket.close();
  }

  /// 连接已经死了但系统还没报错（切网络、手机休眠后的半开连接）：当成意外断开，
  /// 和读出错一样交给 [watchClose] 去重连。
  void drop() => _lost();

  void _lost() {
    if (_ended) return;
    _ended = true;
    _unexpectedLoss = !_closed;
    if (!_closed) {
      _closed = true;
      _failAll(RpcException('closed', 'connection closed'));
      _closeBinary();
      unawaited(_reader.cancel());
      unawaited(socket.close());
    }
    _deliverLoss();
  }

  void _deliverLoss() {
    if (_deliveredLoss || !_unexpectedLoss) return;
    final callback = _onClosed;
    if (callback == null) return;
    _deliveredLoss = true;
    callback();
  }

  Future<void> _sendRequest(
    String id,
    String method,
    Map<String, Object?> params,
  ) {
    final body = jsonEncode({
      'id': id,
      'deviceToken': deviceToken,
      'method': method,
      'params': params,
    });
    return sendPlain(
      Uint8List.fromList(utf8.encode(body)),
      direction: dirMobileToDesktop,
      kind: kindText,
    );
  }

  Future<void> _read() async {
    while (!_closed) {
      final SocketEvent event;
      try {
        event = await _reader.next(CancelToken());
      } catch (_) {
        _lost();
        return;
      }
      if (_closed) return;
      receivedBytes += event.bytes.length;
      try {
        final plain = event.binary
            ? session.open(
                event.bytes,
                direction: dirDesktopToMobile,
                kind: kindBinary,
              )
            : session.openText(event.bytes, direction: dirDesktopToMobile);
        _bad = 0;
        if (event.binary) {
          if (!_binary.isClosed) _binary.add(plain);
          continue;
        }
        final decoded = jsonDecode(utf8.decode(plain));
        if (decoded is Map) _dispatch(decoded.cast<String, Object?>());
      } catch (_) {
        if (++_bad >= 5) {
          _lost();
          return;
        }
      }
    }
  }

  void _dispatch(Map<String, Object?> message) {
    final id = message['id'];
    if (id is! String) return;
    if (message['streaming'] == true) {
      final result = message['result'];
      if (result is Map) {
        _subs[id]?.call(result.cast<String, Object?>());
      }
      return;
    }
    if (message['ok'] != true && _subErr.containsKey(id)) {
      _subErr[id]?.call(_rpcError(message));
      return;
    }
    final waiter = _wait.remove(id);
    if (waiter == null || waiter.isCompleted) return;
    repliesReceived++;
    if (message['ok'] == true) {
      final result = message['result'];
      waiter.complete(
        // 有的方法（如 files.readDir）返回数组，放在 value 里交给调用方
        result is Map
            ? result.cast<String, Object?>()
            : <String, Object?>{'value': result},
      );
    } else {
      waiter.completeError(_rpcError(message));
    }
  }

  RpcException _rpcError(Map<String, Object?> message) {
    final error = message['error'];
    final code = error is Map && error['code'] is String
        ? error['code'] as String
        : 'rpc';
    final text = error is Map && error['message'] is String
        ? error['message'] as String
        : 'rpc failed';
    return RpcException(code, text);
  }

  void _closeBinary() {
    if (!_binary.isClosed) unawaited(_binary.close());
  }

  void _failAll(Object error) {
    final pending = _wait.values.toList();
    _wait.clear();
    for (final waiter in pending) {
      if (!waiter.isCompleted) waiter.completeError(error);
    }
    final rpcError = error is RpcException
        ? error
        : RpcException('closed', 'connection closed');
    for (final fail in _subErr.values.toList()) {
      fail(rpcError);
    }
  }
}
