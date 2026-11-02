import 'dart:convert';
import 'dart:math';
import 'dart:typed_data';

import 'package:crypto/crypto.dart';
import 'package:pinenacl/tweetnacl.dart';
import 'package:posse_quic/posse_quic.dart' as quic;

const transcriptDomain = 'orca-mobile-e2ee/v2/transcript';
const protocolName = 'orca-mobile-e2ee';

const dirMobileToDesktop = 0;
const dirDesktopToMobile = 1;
const kindText = 0;
const kindBinary = 1;
const headerLen = 42;

const payloadKinds = ['text', 'binary'];

/// 分帧（握手里的 framing）：2 是 Orca 的，JSON 加密后再 base64 走文本帧；
/// 3 是 Posse 的，所有帧都是加密后的原始字节、走二进制帧，JSON 和二进制靠 nonce 里的 kind（第 14 字节）区分。
const framingText = 2;
const framingBinary = 3;

class E2eeException implements Exception {
  E2eeException(this.message);
  final String message;
  @override
  String toString() => message;
}

class HelloContext {
  const HelloContext({
    required this.protocol,
    required this.initiator,
    required this.responder,
    required this.transport,
    this.relayHostId,
  });

  final String protocol;
  final String initiator;
  final String responder;
  final String transport;
  final String? relayHostId;

  Map<String, Object?> toJson() => {
    'protocol': protocol,
    'initiator': initiator,
    'responder': responder,
    'transport': transport,
    if (relayHostId != null) 'relayHostId': relayHostId,
  };
}

class Hello {
  const Hello({
    required this.clientPublicKey,
    required this.clientNonce,
    required this.context,
    this.framing = framingBinary,
  });

  final Uint8List clientPublicKey;
  final Uint8List clientNonce;
  final HelloContext context;
  final int framing;

  Map<String, Object?> toJson() => {
    'type': 'e2ee_hello',
    'v': 2,
    'clientPublicKeyB64': b64(clientPublicKey),
    'clientNonceB64': b64(clientNonce),
    'capabilities': {
      'framing': [framing],
      'payloadKinds': payloadKinds,
    },
    'context': context.toJson(),
  };
}

class Ready {
  const Ready({
    required this.desktopPublicKey,
    required this.clientNonce,
    required this.desktopNonce,
    required this.context,
    this.framing = framingBinary,
  });

  final Uint8List desktopPublicKey;
  final Uint8List clientNonce;
  final Uint8List desktopNonce;
  final HelloContext context;
  final int framing;

  Map<String, Object?> toJson() => {
    'type': 'e2ee_ready',
    'v': 2,
    'desktopPublicKeyB64': b64(desktopPublicKey),
    'clientNonceB64': b64(clientNonce),
    'desktopNonceB64': b64(desktopNonce),
    'selection': {'framing': framing, 'payloadKinds': payloadKinds},
    'context': context.toJson(),
  };
}

/// 一条连接握手完成后的加密状态。两个方向各一个计数器，text 和 binary 共用。
class E2eeSession {
  E2eeSession({
    required Uint8List recvKey,
    required Uint8List sendKey,
    required Uint8List id,
    required Uint8List transcriptHash,
  }) : recvKey = Uint8List.fromList(recvKey),
       sendKey = Uint8List.fromList(sendKey),
       id = Uint8List.fromList(id),
       transcriptHash = Uint8List.fromList(transcriptHash);

  final Uint8List recvKey;
  final Uint8List sendKey;
  final Uint8List id;
  final Uint8List transcriptHash;
  int recvN = 0;
  int sendN = 0;

  /// 握手选定的分帧，见 [framingBinary]。
  int framing = framingText;

  /// JSON 消息是不是也走二进制帧（不 base64）。
  bool get binaryMessages => framing == framingBinary;

  /// 手机侧收发密钥对调：主机的 send 是手机的 recv。
  E2eeSession asPhone() => E2eeSession(
    recvKey: sendKey,
    sendKey: recvKey,
    id: id,
    transcriptHash: transcriptHash,
  )..framing = framing;

  Uint8List seal(
    Uint8List payload, {
    required int direction,
    required int kind,
  }) {
    final n = sendN++;
    final nonce = frameNonce(id, direction, kind, n);
    final plain = concat([frameHeader(id, direction, kind, n), payload]);
    return secretboxSeal(sendKey, nonce, plain);
  }

  Uint8List open(Uint8List frame, {required int direction, required int kind}) {
    final nonce = frameNonce(id, direction, kind, recvN);
    if (frame.length < 24 + quic.secretboxOverhead + headerLen ||
        !bytesEqual(frame.sublist(0, 24), nonce)) {
      throw E2eeException('bad frame');
    }
    final plain = secretboxOpen(recvKey, frame);
    final header = frameHeader(id, direction, kind, recvN);
    if (!bytesEqual(plain.sublist(0, headerLen), header)) {
      throw E2eeException('bad frame');
    }
    recvN++;
    return Uint8List.sublistView(plain, headerLen);
  }

  Uint8List openText(Uint8List b64Bytes, {required int direction}) => open(
    base64.decode(latin1.decode(b64Bytes)),
    direction: direction,
    kind: kindText,
  );

  /// 解开收到的一帧，返回它的 kind（[kindText] 是 JSON）和明文。
  (int, Uint8List) openMessage(
    Uint8List bytes, {
    required bool binary,
    required int direction,
  }) {
    if (binaryMessages) {
      if (!binary || bytes.length < 24) throw E2eeException('bad frame');
      final kind = bytes[14];
      if (kind != kindText && kind != kindBinary) {
        throw E2eeException('bad frame');
      }
      return (kind, open(bytes, direction: direction, kind: kind));
    }
    if (binary) {
      return (kindBinary, open(bytes, direction: direction, kind: kindBinary));
    }
    return (kindText, openText(bytes, direction: direction));
  }
}

class KeyPair {
  KeyPair(this.publicKey, this.secretKey);
  final Uint8List publicKey;
  final Uint8List secretKey;
}

KeyPair generateKeyPair() {
  final secret = Uint8List(32);
  final public = Uint8List(32);
  TweetNaCl.crypto_box_keypair(public, secret);
  return KeyPair(Uint8List.fromList(public), Uint8List.fromList(secret));
}

Uint8List randomBytes(int n) {
  final out = Uint8List(n);
  final rand = Random.secure();
  for (var i = 0; i < n; i++) {
    out[i] = rand.nextInt(256);
  }
  return out;
}

/// X25519 之后再做 HSalsa20，和 Go 的 nacl/box.Precompute 一样。
Uint8List beforeNm(Uint8List theirPublic, Uint8List mySecret) {
  final k = Uint8List(32);
  return Uint8List.fromList(
    TweetNaCl.crypto_box_beforenm(k, theirPublic, mySecret),
  );
}

Uint8List encodeTranscript(Hello hello, Ready ready) {
  Uint8List ctx(String prefix, HelloContext c) => concat([
    field('$prefix.context.protocol', utf8Of(c.protocol)),
    field('$prefix.context.initiator', utf8Of(c.initiator)),
    field('$prefix.context.responder', utf8Of(c.responder)),
    field('$prefix.context.transport', utf8Of(c.transport)),
    field('$prefix.context.relay-host-id', utf8Of(c.relayHostId ?? '')),
  ]);
  return concat([
    field('domain', utf8Of(transcriptDomain)),
    field('mobile-to-desktop.type', utf8Of('e2ee_hello')),
    field('mobile-to-desktop.version', u32(2)),
    field('mobile-to-desktop.client-public-key', hello.clientPublicKey),
    field('mobile-to-desktop.client-nonce', hello.clientNonce),
    field(
      'mobile-to-desktop.capabilities.framing',
      encodeNumbers([hello.framing]),
    ),
    field(
      'mobile-to-desktop.capabilities.payload-kinds',
      encodeStrings(payloadKinds),
    ),
    ctx('mobile-to-desktop', hello.context),
    field('desktop-to-mobile.type', utf8Of('e2ee_ready')),
    field('desktop-to-mobile.version', u32(2)),
    field('desktop-to-mobile.desktop-public-key', ready.desktopPublicKey),
    field('desktop-to-mobile.client-nonce-echo', hello.clientNonce),
    field('desktop-to-mobile.desktop-nonce', ready.desktopNonce),
    field('desktop-to-mobile.selection.framing', u32(ready.framing)),
    field(
      'desktop-to-mobile.selection.payload-kinds',
      encodeStrings(payloadKinds),
    ),
    ctx('desktop-to-mobile', ready.context),
  ]);
}

E2eeSession deriveSession({
  required Uint8List shared,
  required Uint8List transcript,
  required Uint8List clientNonce,
  required Uint8List desktopNonce,
}) {
  final transcriptHash = Uint8List.fromList(sha256.convert(transcript).bytes);
  final salt = Uint8List.fromList(
    sha256
        .convert(
          concat([
            utf8Of('orca-mobile-e2ee/v2/salt'),
            Uint8List.fromList([0]),
            clientNonce,
            desktopNonce,
          ]),
        )
        .bytes,
  );
  final info = concat([
    utf8Of('orca-mobile-e2ee/v2/session'),
    Uint8List.fromList([0]),
    transcriptHash,
  ]);
  final k = hkdfSha256(shared, salt, info, 96);
  return E2eeSession(
    recvKey: k.sublist(0, 32),
    sendKey: k.sublist(32, 64),
    id: k.sublist(64),
    transcriptHash: transcriptHash,
  );
}

/// 主机回的明文 ready。只接受和 hello 同一条直连上下文，键必须恰好是协议里的那些。
Ready? parseReady(String raw, Hello hello) {
  Object? decoded;
  try {
    decoded = jsonDecode(raw);
  } catch (_) {
    return null;
  }
  if (decoded is! Map) return null;
  final map = decoded.cast<String, Object?>();
  if (!exactKeys(map, const [
    'type',
    'v',
    'desktopPublicKeyB64',
    'clientNonceB64',
    'desktopNonceB64',
    'selection',
    'context',
  ])) {
    return null;
  }
  if (map['type'] != 'e2ee_ready' || map['v'] != 2) return null;
  if (map['clientNonceB64'] != b64(hello.clientNonce)) return null;
  final selection = map['selection'];
  if (selection is! Map ||
      !exactKeys(selection.cast<String, Object?>(), const [
        'framing',
        'payloadKinds',
      ])) {
    return null;
  }
  final kinds = selection['payloadKinds'];
  if (selection['framing'] != hello.framing ||
      kinds is! List ||
      !_sameStrings(kinds, payloadKinds)) {
    return null;
  }
  final context = map['context'];
  if (context is! Map ||
      !exactKeys(context.cast<String, Object?>(), const [
        'protocol',
        'initiator',
        'responder',
        'transport',
      ])) {
    return null;
  }
  if (context['protocol'] != hello.context.protocol ||
      context['initiator'] != hello.context.initiator ||
      context['responder'] != hello.context.responder ||
      context['transport'] != 'direct' ||
      hello.context.transport != 'direct') {
    return null;
  }
  final desktopPublic = decode32(map['desktopPublicKeyB64']);
  final desktopNonce = decode32(map['desktopNonceB64']);
  if (desktopPublic == null || desktopNonce == null) return null;
  return Ready(
    desktopPublicKey: desktopPublic,
    clientNonce: hello.clientNonce,
    desktopNonce: desktopNonce,
    context: hello.context,
    framing: hello.framing,
  );
}

String b64(List<int> bytes) => base64.encode(bytes);

Uint8List? decode32(Object? value) {
  if (value is! String) return null;
  try {
    final bytes = base64.decode(value);
    if (bytes.length != 32 || base64.encode(bytes) != value) return null;
    return Uint8List.fromList(bytes);
  } catch (_) {
    return null;
  }
}

bool exactKeys(Map<String, Object?> map, List<String> keys) {
  if (map.length != keys.length) return false;
  for (final key in keys) {
    if (!map.containsKey(key)) return false;
  }
  return true;
}

Uint8List hkdfSha256(
  Uint8List ikm,
  Uint8List salt,
  Uint8List info,
  int length,
) {
  final prk = Uint8List.fromList(Hmac(sha256, salt).convert(ikm).bytes);
  final out = BytesBuilder(copy: false);
  var block = Uint8List(0);
  var counter = 1;
  while (out.length < length) {
    final input = concat([
      block,
      info,
      Uint8List.fromList([counter]),
    ]);
    block = Uint8List.fromList(Hmac(sha256, prk).convert(input).bytes);
    out.add(block);
    counter++;
  }
  return Uint8List.sublistView(out.takeBytes(), 0, length);
}

/// Go 的 secretbox.Seal 格式：nonce 拼在密文前面。加解密在 posse_quic 的 Go 里做：纯 Dart 的 TweetNaCl 慢约 5 倍。
Uint8List secretboxSeal(Uint8List key, Uint8List nonce, Uint8List plain) =>
    quic.secretboxSeal(key, nonce, plain);

/// 解开整帧（nonce 加密文），nonce 用帧里的。
Uint8List secretboxOpen(Uint8List key, Uint8List frame) =>
    quic.secretboxOpen(key, frame) ?? (throw E2eeException('bad frame'));

Uint8List frameNonce(Uint8List id, int direction, int kind, int counter) {
  final out = Uint8List(24);
  out.setRange(0, 12, id);
  out[12] = 2;
  out[13] = direction;
  out[14] = kind;
  ByteData.sublistView(out).setUint64(16, counter);
  return out;
}

Uint8List frameHeader(Uint8List id, int direction, int kind, int counter) {
  final out = Uint8List(headerLen);
  out.setRange(0, 32, id);
  out[32] = direction;
  out[33] = kind;
  ByteData.sublistView(out).setUint64(34, counter);
  return out;
}

Uint8List u32(int value) {
  final out = Uint8List(4);
  ByteData.sublistView(out).setUint32(0, value);
  return out;
}

Uint8List utf8Of(String value) => Uint8List.fromList(utf8.encode(value));

Uint8List encodeNumbers(List<int> values) =>
    concat([u32(values.length), ...values.map(u32)]);

Uint8List encodeStrings(List<String> values) {
  final parts = <Uint8List>[u32(values.length)];
  for (final value in values) {
    final bytes = utf8Of(value);
    parts.add(u32(bytes.length));
    parts.add(bytes);
  }
  return concat(parts);
}

Uint8List field(String name, Uint8List value) {
  final key = utf8Of(name);
  return concat([u32(key.length), key, u32(value.length), value]);
}

Uint8List concat(List<Uint8List> parts) {
  var n = 0;
  for (final part in parts) {
    n += part.length;
  }
  final out = Uint8List(n);
  var offset = 0;
  for (final part in parts) {
    out.setRange(offset, offset + part.length, part);
    offset += part.length;
  }
  return out;
}

bool bytesEqual(List<int> a, List<int> b) {
  if (a.length != b.length) return false;
  var diff = 0;
  for (var i = 0; i < a.length; i++) {
    diff |= a[i] ^ b[i];
  }
  return diff == 0;
}

bool _sameStrings(List<Object?> got, List<String> want) {
  if (got.length != want.length) return false;
  for (var i = 0; i < want.length; i++) {
    if (got[i] != want[i]) return false;
  }
  return true;
}

String hexOf(List<int> bytes) =>
    bytes.map((b) => b.toRadixString(16).padLeft(2, '0')).join();

Uint8List repeated(int byte, int length) =>
    Uint8List(length)..fillRange(0, length, byte);
