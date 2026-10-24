import 'dart:io';

/// 这条 WebSocket 能不能走桌面 UDP。
/// 主机本身是局域网、Tailscale（100.64.0.0/10）或回环地址，
/// 或者名字能解析成这类地址，才算直连。模拟器的 127.0.0.1 算。
/// 解析失败、只解析到公网地址，都不订阅。
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
  if (address.isLoopback) return true;
  if (address.type == InternetAddressType.IPv4) {
    return _ipv4Direct(_octets(address.address));
  }
  return _ipv6Direct(address);
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

bool _ipv6Direct(InternetAddress address) {
  final raw = address.rawAddress;
  if (raw.length != 16) return false;
  // fc00::/7 是本地唯一地址，Tailscale 的 IPv6 在里面。fe80::/10 是链路本地。
  if (raw[0] & 0xfe == 0xfc) return true;
  if (raw[0] == 0xfe && raw[1] & 0xc0 == 0x80) return true;
  return false;
}
