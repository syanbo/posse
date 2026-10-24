import 'dart:convert';
import 'dart:typed_data';

import '../protocol/e2ee.dart';

/// 第 3 步和 `tools/deskrecv`、`desk cast` 共用的材料。正式观看不用它。
const deskTestSecret = 'hive-desk-test-secret-32bytes!!';
const deskTestNonce = 'hive-desk-nonce!';

/// Mac 侧 `hive desk cast` 的默认端口。
const deskDefaultPort = 47991;

enum DeskCodec { hevc, h264 }

Uint8List deskTestSecretBytes() =>
    Uint8List.fromList(utf8.encode(deskTestSecret));

Uint8List deskTestNonceBytes() =>
    Uint8List.fromList(utf8.encode(deskTestNonce));

/// 调试里展示的地址。IPv6 要加括号，不然和端口粘在一起。
String deskEndpoint(String host, int port) {
  final shown = host.contains(':') ? '[$host]' : host;
  return '$shown:$port';
}

int? parseDeskPort(String text) {
  final n = int.tryParse(text.trim());
  if (n == null || n < 1 || n > 65535) return null;
  return n;
}

/// 发往手机、发往 Mac 的两把 AES-256 密钥。
class DeskTrafficKeys {
  const DeskTrafficKeys({required this.toPhone, required this.toMac});

  final Uint8List toPhone;
  final Uint8List toMac;
}

/// 材料是这条连接的会话密钥，Mac 发出的在前：手机上 recvKey 再 sendKey。
/// HKDF-SHA256，salt 是订阅的 nonce，info 是 hive-desk/v1。前 32 字节解密，后 32 字节加密。
DeskTrafficKeys deriveDeskKeys({
  required Uint8List recvKey,
  required Uint8List sendKey,
  required Uint8List nonce,
}) {
  if (recvKey.length != 32 || sendKey.length != 32) {
    throw ArgumentError('session keys');
  }
  if (nonce.isEmpty) throw ArgumentError('nonce');
  final ikm = Uint8List(64);
  ikm.setRange(0, 32, recvKey);
  ikm.setRange(32, 64, sendKey);
  final out = hkdfSha256(
    ikm,
    Uint8List.fromList(nonce),
    Uint8List.fromList(utf8.encode('hive-desk/v1')),
    64,
  );
  return DeskTrafficKeys(
    toPhone: Uint8List.fromList(out.sublist(0, 32)),
    toMac: Uint8List.fromList(out.sublist(32, 64)),
  );
}
