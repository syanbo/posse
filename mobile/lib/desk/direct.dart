import 'dart:io';

/// 这条 WebSocket 能不能走桌面 UDP。
/// 主机本身是局域网、Tailscale（100.64.0.0/10）或回环的 IPv4 地址，
/// 或者名字能解析成这类地址，才算直连。模拟器的 127.0.0.1 算。
/// 解析失败、只解析到公网地址，都不订阅。
/// 只认 IPv4：两端原生的收包（DeskSession）只解析、只发 IPv4，IPv6 的地址交过去直接报采集失败，
/// 不会再去试别的地址和中转。
Future<bool> deskHostDirect(
  String host, {
  Future<List<InternetAddress>> Function(String host)? lookup,
}) async {
  if (host.isEmpty) return false;
  final parsed = InternetAddress.tryParse(host);
  if (parsed != null) return deskAddressDirect(parsed);
  try {
    final found = await (lookup ?? InternetAddress.lookup)(host);
    return found.any(deskAddressDirect);
  } catch (_) {
    return false;
  }
}

bool deskAddressDirect(InternetAddress address) {
  if (address.type != InternetAddressType.IPv4) return false;
  return _ipv4Direct(_octets(address.address));
}

bool _ipv4Direct(List<int>? parts) {
  if (parts == null || parts.length != 4) return false;
  final a = parts[0];
  final b = parts[1];
  if (a == 10) return true;
  if (a == 127) return true;
  if (a == 172 && b >= 16 && b <= 31) return true;
  if (a == 192 && b == 168) return true;
  if (a == 169 && b == 254) return true;
  // Tailscale 的 CGNAT：100.64.0.0/10。
  if (a == 100 && b >= 64 && b <= 127) return true;
  return false;
}

List<int>? _octets(String text) {
  final parts = text.split('.');
  if (parts.length != 4) return null;
  final out = <int>[];
  for (final part in parts) {
    final n = int.tryParse(part);
    if (n == null || n < 0 || n > 255) return null;
    out.add(n);
  }
  return out;
}

/// 画面的 UDP 发到哪。当前连接本身能直连就用它；走隧道时从同一台电脑的其他地址
/// （配对码里的地址、另外扫过的局域网码）里挑第一个能直连的：订阅仍走当前连接，
/// 只有画面和输入换路。回环地址跳过，手机上的 127.0.0.1 不是那台 Mac。
Future<String?> deskUdpHost(
  String host,
  Iterable<String> others, {
  Future<List<InternetAddress>> Function(String host)? lookup,
}) async {
  if (await deskHostDirect(host, lookup: lookup)) return host;
  final seen = {host};
  for (final other in others) {
    if (other.isEmpty || other == 'localhost' || !seen.add(other)) continue;
    if (InternetAddress.tryParse(other)?.isLoopback ?? false) continue;
    if (await deskHostDirect(other, lookup: lookup)) return other;
  }
  return null;
}
