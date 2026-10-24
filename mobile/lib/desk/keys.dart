import 'dart:convert';
import 'dart:typed_data';

/// 第 3 步和 `tools/deskrecv`、`desk cast` 共用的材料。HKDF 在原生层做，这里只交出原字节。
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
