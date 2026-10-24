import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:hive_quic/hive_quic.dart';

import 'race.dart';
import 'socket.dart';

/// QUIC 地址的写法：`hquic://主机:端口`，经中转时带 `?relay=令牌`（base64url）。
/// 只在手机里用：电脑经 hive.quic 告诉手机，不进配对码。
const quicScheme = 'hquic';

bool isQuicUrl(String url) => url.startsWith('$quicScheme://');

/// 经中转的 QUIC 地址。
bool isRelayQuicUrl(String url) =>
    isQuicUrl(url) &&
    (Uri.tryParse(url)?.queryParameters.containsKey('relay') ?? false);

/// 把 hive.quic 的回复换成地址：先局域网、Tailscale 各地址，最后中转。
List<String> quicUrlsOf(Map<String, Object?> info) {
  final port = info['port'];
  final out = <String>[];
  if (port is int && port > 0) {
    final hosts = info['hosts'];
    if (hosts is List) {
      for (final host in hosts.whereType<String>()) {
        if (host.isEmpty) continue;
        final h = host.contains(':') ? '[$host]' : host;
        out.add('$quicScheme://$h:$port');
      }
    }
  }
  final relay = info['relay'];
  final token = info['relayToken'];
  if (relay is String &&
      relay.isNotEmpty &&
      token is String &&
      token.isNotEmpty) {
    final raw = base64.decode(token);
    out.add('$quicScheme://$relay?relay=${base64Url.encode(raw)}');
  }
  return out;
}

class QuicByteSocket implements ByteSocket {
  QuicByteSocket(this._conn);

  final QuicConnection _conn;

  static Future<QuicByteSocket> connect(String url, CancelToken cancel) async {
    if (cancel.cancelled) throw const RaceCancelled();
    final uri = Uri.parse(url);
    final relay = uri.queryParameters['relay'];
    final conn = await QuicConnection.dial(
      uri.host,
      uri.port,
      relayToken: relay == null ? null : base64Url.decode(relay),
    );
    if (cancel.cancelled) {
      conn.close();
      throw const RaceCancelled();
    }
    return QuicByteSocket(conn);
  }

  @override
  late final Stream<SocketEvent> events = _conn.messages.map(
    (m) => m.binary ? SocketEvent.binary(m.data) : SocketEvent.text(m.data),
  );

  @override
  Future<void> sendText(String text) async =>
      _conn.send(quicText, Uint8List.fromList(utf8.encode(text)));

  @override
  Future<void> sendBinary(Uint8List bytes) async =>
      _conn.send(quicBinary, bytes);

  @override
  Future<void> close() async => _conn.close();
}

/// 按地址的写法挑传输。
Future<ByteSocket> connectAny(String url, CancelToken cancel) => isQuicUrl(url)
    ? QuicByteSocket.connect(url, cancel)
    : WebSocketByteSocket.connect(url, cancel);
