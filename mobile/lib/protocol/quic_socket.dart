import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:posse_quic/posse_quic.dart';

import 'race.dart';
import 'socket.dart';

/// QUIC 地址的写法：`hquic://主机:端口`，经中转时带 `?relay=令牌`（base64url）。
/// 只在手机里用：电脑经 posse.quic 告诉手机，不进配对码。
const quicScheme = 'hquic';

/// 经内置 Tailscale（tsnet 用户态 UDP）拨号的 QUIC 地址：`tsquic://100.x.y.z:端口`。
/// 仅在手机内置 Tailscale 进入 Running 时由现有地址派生，不落盘、不进配对码。
const tsQuicScheme = 'tsquic';

bool isQuicUrl(String url) => url.startsWith('$quicScheme://');

bool isTsQuicUrl(String url) => url.startsWith('$tsQuicScheme://');

/// 属于 QUIC 族的传输（含普通 hquic:// 与内置 Tailscale tsquic://）：
/// 竞速时免 2 秒 quicHeadStart，赢了之后不触发 _learnQuic 升级。
bool isAnyQuicUrl(String url) => isQuicUrl(url) || isTsQuicUrl(url);

/// 判断主机字符串是否是 Tailscale IPv4 段（100.64.0.0/10）。
bool isTailscaleIp(String host) {
  final parts = host.trim().split('.');
  if (parts.length != 4) return false;
  final a = int.tryParse(parts[0]);
  final b = int.tryParse(parts[1]);
  final c = int.tryParse(parts[2]);
  final d = int.tryParse(parts[3]);
  if (a == null || b == null || c == null || d == null) return false;
  if (a < 0 || a > 255 || b < 0 || b > 255 || c < 0 || c > 255 || d < 0 || d > 255) {
    return false;
  }
  return a == 100 && b >= 64 && b <= 127;
}

/// 从已有地址（posse.quic 给的 quicUrls 与配对码 offer.dialUrls）中的 100.64/10 地址
/// 派生出 `tsquic://100.x:port` 候选（去重保序，跳过中转与打洞地址）。
List<String> deriveTsQuicUrls(Iterable<String> urls) {
  final out = <String>[];
  final seen = <String>{};
  for (final raw in urls) {
    if (isRelayQuicUrl(raw) || isPunchedQuicUrl(raw)) continue;
    final uri = Uri.tryParse(raw);
    if (uri == null || !uri.hasPort || uri.port <= 0) continue;
    if (!isTailscaleIp(uri.host)) continue;
    final derived = '$tsQuicScheme://${uri.host}:${uri.port}';
    if (seen.add(derived)) out.add(derived);
  }
  return out;
}

/// 打洞直连上的地址：电脑中转出口的公网地址。只用来显示，不存、不重连（每次要先经中转重新打洞）。
String punchedUrl(String peer) => '$quicScheme://$peer?punched=1';

bool isPunchedQuicUrl(String url) =>
    isQuicUrl(url) &&
    (Uri.tryParse(url)?.queryParameters.containsKey('punched') ?? false);

/// 经中转的 QUIC 地址。
bool isRelayQuicUrl(String url) =>
    isQuicUrl(url) &&
    (Uri.tryParse(url)?.queryParameters.containsKey('relay') ?? false);

/// 把 posse.quic 的回复换成地址：先局域网、Tailscale 各地址，最后中转。
List<String> quicUrlsOf(Map<String, Object?> info) {
  final port = info['port'];
  final out = <String>[];
  if (port is int && port > 0) {
    final hosts = info['hosts'];
    if (hosts is List) {
      for (final host in hosts.whereType<String>()) {
        if (host.isEmpty) continue;
        final h = host.contains(':') ? '[$host]' : host;
        final u = '$quicScheme://$h:$port';
        if (!out.contains(u)) out.add(u);
      }
    }
    final tsHost = info['tsHost'];
    if (tsHost is String && tsHost.isNotEmpty) {
      final h = tsHost.contains(':') ? '[$tsHost]' : tsHost;
      final u = '$quicScheme://$h:$port';
      if (!out.contains(u)) out.add(u);
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
    final QuicConnection conn;
    if (isTsQuicUrl(url)) {
      conn = await QuicConnection.dialTailscale(uri.host, uri.port);
    } else {
      final relay = uri.queryParameters['relay'];
      conn = await QuicConnection.dial(
        uri.host,
        uri.port,
        relayToken: relay == null ? null : base64Url.decode(relay),
      );
    }
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

  @override
  int? get closeCode => _conn.closeCode;
}

/// [QuicPuncher] 的实现，见 [QuicConnection.prepare]。
Future<(ByteSocket, String)> punchQuic(
  String relayUrl,
  Future<String> Function(String mine) exchange,
) async {
  final relay = Uri.parse(relayUrl);
  final punch = await QuicConnection.prepare(relay.host, relay.port);
  try {
    final peer = await exchange(punch.public);
    return (QuicByteSocket(await punch.dial(peer)), peer);
  } catch (_) {
    punch.close();
    rethrow;
  }
}

/// 按地址的写法挑传输。
Future<ByteSocket> connectAny(String url, CancelToken cancel) =>
    isAnyQuicUrl(url)
    ? QuicByteSocket.connect(url, cancel)
    : WebSocketByteSocket.connect(url, cancel);
