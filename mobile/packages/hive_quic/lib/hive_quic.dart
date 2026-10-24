/// Hive 手机端的 QUIC 连接。协议见仓库里的 internal/mquic：一条流上收发文本、二进制两种消息。
@DefaultAsset('package:hive_quic/hive_quic.dart')
library;

import 'dart:async';
import 'dart:ffi';
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

@Native<Int32 Function(Int64, Int32, Pointer<Uint8>, Int32)>(symbol: 'hq_send')
external int _send(int id, int type, Pointer<Uint8> data, int len);

@Native<Void Function(Int64, Int32, Pointer<Utf8>)>(symbol: 'hq_close')
external void _close(int id, int code, Pointer<Utf8> reason);

@Native<Void Function(Pointer<Uint8>)>(symbol: 'hq_free')
external void _free(Pointer<Uint8> p);

const _evOpen = 1;
const _evText = 2;
const _evBinary = 3;
const _evClosed = 4;
const _evFailed = 5;

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
final _callback = NativeCallable<_EventFn>.listener(_onEvent);
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
  QuicConnection._(this._id);

  final int _id;
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

  /// 收到的消息。连接断开时结束。
  Stream<QuicMessage> get messages => _messages.stream;

  void _event(int kind, Uint8List data) {
    switch (kind) {
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
