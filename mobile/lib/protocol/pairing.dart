import 'dart:convert';

/// 二维码里的配对信息。Orca 只读 endpoint；endpoints 是 Posse 客户端要并发尝试的地址。
class PairingOffer {
  const PairingOffer({
    required this.endpoint,
    required this.endpoints,
    required this.deviceToken,
    required this.publicKeyB64,
  });

  final String endpoint;
  final List<String> endpoints;
  final String deviceToken;
  final String publicKeyB64;

  /// 主地址在前，其余去重。没有 endpoints 时只连 endpoint。
  List<String> get dialUrls {
    final out = <String>[];
    void add(String value) {
      final trimmed = value.trim();
      if (trimmed.isEmpty || out.contains(trimmed)) return;
      out.add(trimmed);
    }

    add(endpoint);
    for (final extra in endpoints) {
      add(extra);
    }
    return out;
  }

  static PairingOffer parse(String raw) {
    var text = raw.trim();
    final uri = Uri.tryParse(text);
    if (uri != null &&
        (uri.scheme == 'orca' || uri.queryParameters.containsKey('code'))) {
      final code = uri.queryParameters['code'];
      if (code != null && code.isNotEmpty) text = code;
    }
    final bytes = base64Url.decode(base64Url.normalize(text));
    final decoded = jsonDecode(utf8.decode(bytes));
    if (decoded is! Map) {
      throw const FormatException('pairing offer is not an object');
    }
    final map = decoded.cast<String, Object?>();
    if (map['v'] != 2) {
      throw FormatException('pairing offer version ${map['v']}');
    }
    final endpoint = map['endpoint'];
    final token = map['deviceToken'];
    final publicKey = map['publicKeyB64'];
    if (endpoint is! String ||
        endpoint.isEmpty ||
        token is! String ||
        token.isEmpty ||
        publicKey is! String ||
        publicKey.isEmpty) {
      throw const FormatException('pairing offer is missing endpoint or token');
    }
    final extras = <String>[];
    final listed = map['endpoints'];
    if (listed is List) {
      for (final item in listed) {
        if (item is String && item.isNotEmpty) extras.add(item);
      }
    }
    return PairingOffer(
      endpoint: endpoint,
      endpoints: extras,
      deviceToken: token,
      publicKeyB64: publicKey,
    );
  }
}
