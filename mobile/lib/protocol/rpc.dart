import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'e2ee.dart';
import 'race.dart';
import '../l10n/l10n.dart';
import 'socket.dart';

const posseClientName = 'posse-flutter';
const posseClientVersion = 1;
const posseCapabilities = [
  'posse.permission.v1',
  'posse.git.v1',
  'posse.git.v2',
  'posse.git.v3',
  'posse.history.v1',
  'posse.discussions.v1',
  'posse.status.v1',
  'posse.draft.v1',
  'posse.usage.v1',
  'posse.context.v1',
  'posse.media.v1',
  'posse.tools.v1',
  'posse.desk.v1',
  'posse.bulk.v1',
  'posse.push.v1',
  'posse.termcolors.v1',
];

/// 大块数据的二进制帧（电脑的 internal/mobile/bulk.go）：u8 0x62、u8 版本 1、u8 操作、u8 0、
/// u32 编号（小端）、内容。电脑先把回复里的大字段按块发来，回复里写 `blobs: {字段路径: 编号}`；
/// 上传的分段由手机按同样的格式发给电脑。
/// 收到的 JSON 直接从 UTF-8 字节解析，不先转成字符串。
final _jsonUtf8 = utf8.decoder.fuse(json.decoder);

const bulkMagic = 0x62;
const bulkHeader = 8;
const opBlobChunk = 1;
const opUploadChunk = 2;
const opUploadAck = 3;
const bulkChunk = 64 * 1024;

Uint8List bulkFrame(int op, int id, List<int> payload) {
  final out = Uint8List(bulkHeader + payload.length);
  out[0] = bulkMagic;
  out[1] = 1;
  out[2] = op;
  ByteData.sublistView(out).setUint32(4, id, Endian.little);
  out.setRange(bulkHeader, out.length, payload);
  return out;
}

/// 回复里的二进制字段：声明了 posse.bulk.v1 的连接上是 [Uint8List]，旧电脑上是 base64 字符串。
Uint8List rpcBytes(Object? value) {
  if (value is Uint8List) return value;
  if (value is String && value.isNotEmpty) return base64Decode(value);
  return Uint8List(0);
}

class RpcException implements Exception {
  RpcException(this.code, this.message, [this.data]);
  final Object? data;
  final String code;
  final String message;

  /// 电脑上的 posse 没有这个方法（太旧）。Posse 回 method_not_found，沿用 Orca 协议的旧版本回 unknown_method。
  bool get missingMethod =>
      code == 'method_not_found' || code == 'unknown_method';
  @override
  String toString() => '$code: $message';
}

/// [PhoneRpc.upload] 一开始就被拒：这条连接正在换下来或已经关了，一个字节都还没传。
/// 换到当前连接重来是安全的（见 uploadOnCurrent）；传到一半断开的不是它，照常报错。
class UploadRefused extends RpcException {
  UploadRefused() : super('closed', 'connection closed');
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

  /// 还没等到回复的大块字段，按编号攒着。
  final _blobs = <int, BytesBuilder>{};

  /// 解开后的二进制帧（Orca 终端流，16 字节头还在）。先听再订阅，快照才不会丢。
  Stream<Uint8List> get binary => _binary.stream;
  var _n = 0;
  var _bad = 0;

  /// 收到、发出的帧一共多少字节。测延迟时用来判断这段时间连接上有没有别的大块数据在排队；
  /// 上传图片等确认时用来看连接上还在不在收东西。算不算没响应看 [repliesReceived]，不看它。
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

  /// [phone] 是 Posse 扩展：这台手机的安装 ID、型号和存着的这台电脑的全部 token，
  /// 电脑按它把同一台手机的配对合成一项、作废换掉的 token。
  Future<void> declareCapabilities({Map<String, Object?>? phone}) async {
    try {
      await call('runtime.clientCapabilities.update', {
        'client': posseClientName,
        'version': posseClientVersion,
        'capabilities': posseCapabilities,
        'locale': posseLocale(),
        'phone': ?phone,
      });
    } on RpcException catch (error) {
      // 旧常驻进程没有这条扩展，照常使用它已经有的方法。
      if (error.missingMethod) {
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
    // 正在换下来（closeWhenIdle）：新的订阅走新连接
    if (_draining) throw RpcException('closed', 'connection closed');
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
      await socket.sendBinary(frame);
    });
    _tail = result.then((_) {}, onError: (_) {});
    return result;
  }

  /// 正在跑的上传数（[upload]）。上传的编号和分段是这条连接上的，换不到别的连接。
  int get uploading => _uploads;
  var _uploads = 0;

  /// 已经在换下来（[closeWhenIdle]）：不再接新的上传和订阅。
  var _draining = false;

  /// 在这条连接上跑一次上传：[closeWhenIdle] 等它跑完再关，不受 max 限制。
  /// 连接已经在换下来或关了时直接报 closed，新的上传走新连接。
  Future<T> upload<T>(Future<T> Function() body) async {
    if (_closed || _draining) throw UploadRefused();
    _uploads++;
    try {
      return await body();
    } finally {
      _uploads--;
    }
  }

  /// 换到更好的连接时关旧的：等页面在新连接上订阅好、退掉这里的订阅，进行中的请求也回完，再关（最多 [max]）。
  /// 至少等 [min]：页面换连接要等到下一帧，一开始这里看着是空闲的。直接关的话，电脑先收到断开、
  /// 把终端改回电脑上的尺寸，再被新订阅改回手机尺寸，agent 白白重画两次。
  /// 有上传在跑时不管 [max]，等上传跑完（或这条连接自己断了）再关：上传换不到新连接上。
  Future<void> closeWhenIdle({
    Duration min = const Duration(milliseconds: 500),
    Duration max = const Duration(seconds: 10),
  }) async {
    _draining = true;
    final deadline = DateTime.now().add(max);
    await Future<void>.delayed(min);
    while (!_ended &&
        (_uploads > 0 ||
            (_wait.isNotEmpty || _subs.isNotEmpty) &&
                DateTime.now().isBefore(deadline))) {
      await Future<void>.delayed(const Duration(milliseconds: 100));
    }
    await close();
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
      utf8.encode(body),
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
        final (kind, plain) = session.openMessage(
          event.bytes,
          binary: event.binary,
          direction: dirDesktopToMobile,
        );
        _bad = 0;
        if (kind == kindBinary) {
          if (plain.length >= bulkHeader &&
              plain[0] == bulkMagic &&
              plain[1] == 1) {
            final view = ByteData.sublistView(plain);
            final id = view.getUint32(4, Endian.little);
            if (plain[2] == opBlobChunk) {
              (_blobs[id] ??= BytesBuilder(
                copy: false,
              )).add(Uint8List.sublistView(plain, bulkHeader));
            } else if (plain[2] == opUploadAck &&
                plain.length >= bulkHeader + 8) {
              _uploadAcks[id]?.ack(view.getUint64(bulkHeader, Endian.little));
            }
            continue;
          }
          if (!_binary.isClosed) _binary.add(plain);
          continue;
        }
        final decoded = _jsonUtf8.convert(plain);
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
    final result = message['result'];
    if (result is Map) _fillBlobs(result.cast<String, Object?>());
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

  /// 把 `blobs` 里点连起来的字段路径换回攒好的字节。调用方已经超时走掉的也要取走，免得一直攒着。
  void _fillBlobs(Map<String, Object?> result) {
    final blobs = result.remove('blobs');
    if (blobs is! Map) return;
    for (final MapEntry(:key, :value) in blobs.entries) {
      if (key is! String || value is! num) continue;
      final bytes = _blobs.remove(value.toInt())?.takeBytes() ?? Uint8List(0);
      final path = key.split('.');
      Map<Object?, Object?> at = result;
      for (final part in path.take(path.length - 1)) {
        final next = at[part];
        if (next is! Map) break;
        at = next;
      }
      at[path.last] = bytes;
    }
  }

  /// 电脑确认上传已收下的字节数（累计值），见 [watchUploadAcks]。
  final _uploadAcks =
      <int, ({void Function(int) ack, void Function() closed})>{};

  /// 收编号为 [blob] 的上传的确认，返回取消函数。连接断开时调一次 [onClosed]，之后不再调 [onAck]。
  void Function() watchUploadAcks(
    int blob,
    void Function(int got) onAck,
    void Function() onClosed,
  ) {
    final entry = (ack: onAck, closed: onClosed);
    if (_closed) {
      onClosed();
      return () {};
    }
    _uploadAcks[blob] = entry;
    return () {
      if (identical(_uploadAcks[blob], entry)) _uploadAcks.remove(blob);
    };
  }

  /// 上传的一段（编号由 clipboard.startImageUpload 给），和请求共用发送顺序。
  Future<void> sendUploadChunk(int blob, List<int> payload) => sendPlain(
    bulkFrame(opUploadChunk, blob, payload),
    direction: dirMobileToDesktop,
    kind: kindBinary,
  );

  RpcException _rpcError(Map<String, Object?> message) {
    final error = message['error'];
    final code = error is Map && error['code'] is String
        ? error['code'] as String
        : 'rpc';
    final text = error is Map && error['message'] is String
        ? error['message'] as String
        : 'rpc failed';
    return RpcException(code, text, error is Map ? error['data'] : null);
  }

  void _closeBinary() {
    _blobs.clear();
    final waiting = _uploadAcks.values.toList();
    _uploadAcks.clear();
    for (final entry in waiting) {
      entry.closed();
    }
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
