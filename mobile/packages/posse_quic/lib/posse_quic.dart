/// Posse 手机端编进 App 的 Go 库：QUIC 连接、E2EE 的 secretbox 加解密（[secretboxSeal]），和 Mermaid 图的排版（[renderMermaid]）。
/// QUIC 的协议见仓库里的 internal/mquic：一条流上收发文本、二进制两种消息。
@DefaultAsset('package:posse_quic/posse_quic.dart')
library;

import 'dart:async';
import 'dart:ffi';
import 'dart:isolate';
import 'dart:typed_data';

import 'package:ffi/ffi.dart';

typedef _EventFn = Void Function(Int64, Int32, Pointer<Uint8>, Int32);

@Native<
  Int64 Function(
    Pointer<Utf8>,
    Pointer<Uint8>,
    Int32,
    Int32,
    Pointer<NativeFunction<_EventFn>>,
  )
>(symbol: 'hq_dial')
external int _dial(
  Pointer<Utf8> addr,
  Pointer<Uint8> token,
  int tokenLen,
  int timeoutMs,
  Pointer<NativeFunction<_EventFn>> cb,
);

@Native<
  Int64 Function(Pointer<Utf8>, Int32, Pointer<NativeFunction<_EventFn>>)
>(symbol: 'hq_prepare')
external int _prepare(
  Pointer<Utf8> relay,
  int timeoutMs,
  Pointer<NativeFunction<_EventFn>> cb,
);

@Native<Void Function(Int64, Pointer<Utf8>, Int32)>(symbol: 'hq_dial_prepared')
external void _dialPrepared(int id, Pointer<Utf8> peer, int timeoutMs);

@Native<Int32 Function(Int64, Int32, Pointer<Uint8>, Int32)>(symbol: 'hq_send')
external int _send(int id, int type, Pointer<Uint8> data, int len);

@Native<Void Function(Int64, Int32, Pointer<Utf8>)>(symbol: 'hq_close')
external void _close(int id, int code, Pointer<Utf8> reason);

@Native<Void Function()>(symbol: 'hq_reset')
external void _reset();

@Native<Void Function(Pointer<Uint8>)>(symbol: 'hq_free')
external void _free(Pointer<Uint8> p);

@Native<
  Void Function(Pointer<Uint8>, Pointer<Uint8>, Pointer<Uint8>, Int32, Pointer<Uint8>)
>(symbol: 'hq_seal', isLeaf: true)
external void _seal(
  Pointer<Uint8> key,
  Pointer<Uint8> nonce,
  Pointer<Uint8> msg,
  int len,
  Pointer<Uint8> out,
);

@Native<Int32 Function(Pointer<Uint8>, Pointer<Uint8>, Int32, Pointer<Uint8>)>(
  symbol: 'hq_open',
  isLeaf: true,
)
external int _open(
  Pointer<Uint8> key,
  Pointer<Uint8> frame,
  int len,
  Pointer<Uint8> out,
);

/// secretbox 的开销：Poly1305 标签 16 字节，nonce 24 字节拼在密文前面。
const secretboxOverhead = 16;
const secretboxNonceLength = 24;

/// 用 Go 的 nacl/secretbox 加密：返回 nonce 加密文，和 Go 的 `secretbox.Seal(nonce[:], …)` 同格式。
/// 直接把 Dart 的缓冲区交给 Go，不拷贝。
Uint8List secretboxSeal(Uint8List key, Uint8List nonce, Uint8List plain) {
  if (key.length != 32 || nonce.length != secretboxNonceLength) {
    throw ArgumentError('secretbox: bad key or nonce length');
  }
  final out = Uint8List(
    secretboxNonceLength + secretboxOverhead + plain.length,
  );
  _seal(key.address, nonce.address, plain.address, plain.length, out.address);
  return out;
}

/// 解开 [secretboxSeal] 的结果（nonce 取自 frame 的前 24 字节），认证失败返回 null。
Uint8List? secretboxOpen(Uint8List key, Uint8List frame) {
  if (key.length != 32) throw ArgumentError('secretbox: bad key length');
  if (frame.length < secretboxNonceLength + secretboxOverhead) return null;
  final out = Uint8List(
    frame.length - secretboxNonceLength - secretboxOverhead,
  );
  final ok = _open(key.address, frame.address, frame.length, out.address);
  return ok == 1 ? out : null;
}

@Native<Pointer<Utf8> Function(Pointer<Utf8>, Int32)>(symbol: 'hq_mermaid')
external Pointer<Utf8> _mermaid(Pointer<Utf8> request, int len);

/// 画一张 Mermaid 图：[request] 和返回值都是 JSON，格式见 go/mermaid.go 的 mermaidRequest、mermaidResult。
/// 在后台 isolate 里同步调 Go：第一次要启动 JS 运行时（约 100 ms），不能卡界面线程。
/// Go 一次只画一张，这里也排队一张张来，不同时开一堆 isolate 卡在 Go 的锁上。
Future<String> renderMermaid(String request) {
  final done = Completer<String>();
  _mermaidWaiting.add((request, done));
  _nextMermaid();
  return done.future;
}

final _mermaidWaiting = <(String, Completer<String>)>[];
var _mermaidBusy = false;

void _nextMermaid() {
  if (_mermaidBusy || _mermaidWaiting.isEmpty) return;
  _mermaidBusy = true;
  final (request, done) = _mermaidWaiting.removeAt(0);
  _renderMermaid(request)
      .then(done.complete, onError: done.completeError)
      .whenComplete(() {
        _mermaidBusy = false;
        _nextMermaid();
      });
}

Future<String> _renderMermaid(String request) => Isolate.run(() {
  final req = request.toNativeUtf8();
  try {
    final out = _mermaid(req, req.length);
    final text = out.toDartString();
    _free(out.cast());
    return text;
  } finally {
    malloc.free(req);
  }
});

const _evOpen = 1;
const _evText = 2;
const _evBinary = 3;
const _evClosed = 4;
const _evFailed = 5;
const _evPrepared = 6;

/// 消息类型，和 Go 那边一致。
const quicText = 1;
const quicBinary = 2;

class QuicMessage {
  const QuicMessage(this.type, this.data);
  final int type;
  final Uint8List data;
  bool get binary => type == quicBinary;
}

class QuicException implements Exception {
  QuicException(this.message);
  final String message;
  @override
  String toString() => 'QuicException: $message';
}

/// 所有连接共用一个回调。Go 从任意线程调它，事件按发生顺序进 Dart 的事件循环。
/// 第一次用时先断掉进程里上一个 Flutter 引擎留下的连接：它们的回调已经失效（见 hq_reset），
/// 安卓原生层在引擎销毁前已经断过一次，这里兜底。
final _callback = () {
  _reset();
  return NativeCallable<_EventFn>.listener(_onEvent);
}();
final _live = <int, QuicConnection>{};

void _onEvent(int id, int kind, Pointer<Uint8> data, int len) {
  Uint8List bytes;
  if (data == nullptr) {
    bytes = Uint8List(0);
  } else {
    bytes = Uint8List.fromList(data.asTypedList(len));
    _free(data);
  }
  _live[id]?._event(kind, bytes);
}

class QuicConnection {
  QuicConnection._(this._id) {
    // 结束时两个都会带上错误，没人等的那个（普通拨号不经 prepare）不能变成未处理的错误
    _prepared.future.ignore();
    _opened.future.ignore();
  }

  final int _id;
  final _prepared = Completer<String>();
  final _opened = Completer<void>();
  final _messages = StreamController<QuicMessage>();
  var _done = false;

  /// 连到 [host]:[port]。给了 [relayToken] 时那个地址是中转，先登记再握手。
  static Future<QuicConnection> dial(
    String host,
    int port, {
    Uint8List? relayToken,
    Duration timeout = const Duration(seconds: 8),
  }) async {
    final addr = (host.contains(':') ? '[$host]:$port' : '$host:$port')
        .toNativeUtf8();
    final token = relayToken ?? Uint8List(0);
    final tok = malloc<Uint8>(token.isEmpty ? 1 : token.length);
    tok.asTypedList(token.length).setAll(0, token);
    try {
      final id = _dial(
        addr,
        tok,
        token.length,
        timeout.inMilliseconds,
        _callback.nativeFunction,
      );
      final conn = QuicConnection._(id);
      _live[id] = conn;
      await conn._opened.future;
      return conn;
    } finally {
      malloc.free(addr);
      malloc.free(tok);
    }
  }

  /// 打洞的第一步：开一个 socket，经中转（[relayHost]:[relayPort]）问出它在公网上的地址。
  /// 把 [QuicPunch.public] 交给电脑（posse.punch），拿到电脑的地址后 [QuicPunch.dial]。
  static Future<QuicPunch> prepare(
    String relayHost,
    int relayPort, {
    Duration timeout = const Duration(seconds: 3),
  }) async {
    final relay = '$relayHost:$relayPort'.toNativeUtf8();
    try {
      final id = _prepare(
        relay,
        timeout.inMilliseconds,
        _callback.nativeFunction,
      );
      final conn = QuicConnection._(id);
      _live[id] = conn;
      return QuicPunch._(conn, await conn._prepared.future);
    } finally {
      malloc.free(relay);
    }
  }

  /// 收到的消息。连接断开时结束。
  Stream<QuicMessage> get messages => _messages.stream;

  void _event(int kind, Uint8List data) {
    switch (kind) {
      case _evPrepared:
        _prepared.complete(String.fromCharCodes(data));
      case _evOpen:
        _opened.complete();
      case _evText:
      case _evBinary:
        _messages.add(
          QuicMessage(kind == _evText ? quicText : quicBinary, data),
        );
      case _evFailed:
      case _evClosed:
        _done = true;
        _live.remove(_id);
        final reason = String.fromCharCodes(data);
        if (!_prepared.isCompleted) {
          _prepared.completeError(QuicException(reason));
        }
        if (!_opened.isCompleted) _opened.completeError(QuicException(reason));
        unawaited(_messages.close());
    }
  }

  /// 排进发送队列。连接已经断了时抛 [QuicException]。
  void send(int type, Uint8List data) {
    if (_done) throw QuicException('closed');
    final p = malloc<Uint8>(data.isEmpty ? 1 : data.length);
    try {
      p.asTypedList(data.length).setAll(0, data);
      if (_send(_id, type, p, data.length) != 0) throw QuicException('closed');
    } finally {
      malloc.free(p);
    }
  }

  /// 发完已经交出去的消息后关闭。之后 [messages] 会结束。
  void close([int code = 1000, String reason = '']) {
    if (_done) return;
    final r = reason.toNativeUtf8();
    try {
      _close(_id, code, r);
    } finally {
      malloc.free(r);
    }
  }
}

/// 已经问出公网地址、等着打洞的 socket，见 [QuicConnection.prepare]。
class QuicPunch {
  QuicPunch._(this._conn, this.public);

  final QuicConnection _conn;

  /// 这个 socket 在公网上的地址，"ip:端口"。
  final String public;

  /// 向电脑的公网地址 [peer]（"ip:端口"）打洞并拨号。打不通时 [timeout] 到期抛 [QuicException]。
  Future<QuicConnection> dial(
    String peer, {
    Duration timeout = const Duration(seconds: 3),
  }) async {
    final p = peer.toNativeUtf8();
    try {
      _dialPrepared(_conn._id, p, timeout.inMilliseconds);
    } finally {
      malloc.free(p);
    }
    await _conn._opened.future;
    return _conn;
  }

  /// 不打洞了，关掉这个 socket。
  void close() => _conn.close();
}
