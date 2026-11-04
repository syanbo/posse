// 手机端收发一条 JSON 消息的开销：改用 Go 加解密、二进制分帧之前（legacy）和现在对照。
// Mac 上在 mobile/ 下 fvm dart run benchmark/e2ee_bench.dart（JIT，只看倍数）；
// 手机上用 benchmark/app.dart，做法见 mobile/README.md。
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:pinenacl/tweetnacl.dart';

// 手机上用一个临时的小工程跑（见 run-device.sh），它不依赖 posse_mobile，所以按路径引用
// ignore: avoid_relative_lib_imports
import '../lib/protocol/e2ee.dart';

import 'package:posse_quic/posse_quic.dart' as quic;

const sizes = [300, 9 << 10, 64 << 10, 1 << 20];

final _jsonUtf8 = utf8.decoder.fuse(json.decoder);

class BenchRow {
  BenchRow(this.size, this.op, this.micros);
  final int size;
  final String op;
  final double micros;

  double get mbps => size / micros;

  @override
  String toString() =>
      '${'$size'.padLeft(8)}  ${op.padRight(18)}'
      '${micros.toStringAsFixed(micros < 10 ? 2 : 1).padLeft(10)} µs'
      '${mbps.toStringAsFixed(1).padLeft(9)} MB/s';
}

/// 预热后取 21 批的中位数，每批至少约 4 ms，结果是单次的微秒数。
double median(void Function() body) {
  var reps = 1;
  while (true) {
    final sw = Stopwatch()..start();
    for (var i = 0; i < reps; i++) {
      body();
    }
    if (sw.elapsedMicroseconds >= 4000) break;
    reps *= 2;
  }
  for (var i = 0; i < 3; i++) {
    for (var j = 0; j < reps; j++) {
      body();
    }
  }
  final samples = <double>[];
  for (var i = 0; i < 21; i++) {
    final sw = Stopwatch()..start();
    for (var j = 0; j < reps; j++) {
      body();
    }
    samples.add(sw.elapsedMicroseconds / reps);
  }
  samples.sort();
  return samples[samples.length ~/ 2];
}

// 以下照抄 0230333 之前手机上的做法（git show 0230333^:mobile/lib/protocol/），拷贝次数也一样。
// secretbox 是纯 Dart 的 TweetNaCl：明文前垫 32 个 0，它返回的结果再拷一次。
Uint8List tweetSeal(Uint8List key, Uint8List nonce, Uint8List plain) {
  final message = Uint8List(TweetNaCl.zerobytesLength + plain.length);
  message.setRange(TweetNaCl.zerobytesLength, message.length, plain);
  final box = Uint8List(message.length);
  return Uint8List.fromList(
    TweetNaCl.crypto_secretbox(box, message, message.length, nonce, key),
  );
}

Uint8List tweetOpen(Uint8List key, Uint8List nonce, Uint8List box) {
  final cipher = Uint8List(TweetNaCl.boxzerobytesLength + box.length);
  cipher.setRange(TweetNaCl.boxzerobytesLength, cipher.length, box);
  final opened = Uint8List(cipher.length);
  return Uint8List.fromList(
    TweetNaCl.crypto_secretbox_open(opened, cipher, cipher.length, nonce, key),
  );
}

// legacy 是 Orca App 用的分帧 2，App 和电脑都已不再支持，这里留一份只为对照：
// 明文前 42 字节帧头（会话 ID、方向、kind、计数器），nonce 拼在帧前，整帧 base64 走 WebSocket 文本帧（String）。
const headerLen = 42;

Uint8List frameHeader(Uint8List id, int direction, int kind, int counter) {
  final out = Uint8List(headerLen);
  out.setRange(0, 32, id);
  out[32] = direction;
  out[33] = kind;
  ByteData.sublistView(out).setUint64(34, counter);
  return out;
}

String legacySend(E2eeSession s, Map<String, Object?> msg) {
  final payload = Uint8List.fromList(utf8.encode(jsonEncode(msg)));
  final n = s.sendN++;
  final nonce = frameNonce(s.id, dirMobileToDesktop, kindText, n);
  final plain = concat([
    frameHeader(s.id, dirMobileToDesktop, kindText, n),
    payload,
  ]);
  return base64.encode(concat([nonce, tweetSeal(s.sendKey, nonce, plain)]));
}

// 收到的文本帧是 String：socket 先转成字节，openText 再转回字符串做 base64 解码。
Object? legacyRecv(E2eeSession s, String event) {
  final b64Bytes = utf8.encode(event);
  final frame = Uint8List.fromList(base64.decode(utf8.decode(b64Bytes)));
  final nonce = frameNonce(s.id, dirDesktopToMobile, kindText, s.recvN);
  if (frame.length < 24 + TweetNaCl.macBytes + headerLen ||
      !bytesEqual(frame.sublist(0, 24), nonce)) {
    throw StateError('bad frame');
  }
  final plain = tweetOpen(s.recvKey, nonce, frame.sublist(24));
  final header = frameHeader(s.id, dirDesktopToMobile, kindText, s.recvN);
  if (!bytesEqual(plain.sublist(0, headerLen), header)) {
    throw StateError('bad frame');
  }
  s.recvN++;
  return jsonDecode(utf8.decode(Uint8List.sublistView(plain, headerLen)));
}

Map<String, Object?> message(int size) {
  final text = String.fromCharCodes(List.generate(size, (i) => 0x61 + i % 26));
  return {
    'id': 1,
    'result': {'text': text},
  };
}

List<BenchRow> runE2eeBench([void Function(BenchRow)? onRow]) {
  final rows = <BenchRow>[];
  void add(int size, String op, double micros) {
    final row = BenchRow(size, op, micros);
    rows.add(row);
    onRow?.call(row);
  }

  E2eeSession session() => E2eeSession(
    recvKey: randomBytes(32),
    sendKey: randomBytes(32),
    id: randomBytes(32),
    transcriptHash: randomBytes(32),
  );

  for (final size in sizes) {
    final key = randomBytes(32);
    final nonce = randomBytes(24);
    final plain = randomBytes(size);
    final tweetBox = tweetSeal(key, nonce, plain);
    final goBox = quic.secretboxSeal(key, nonce, plain);
    if (!bytesEqual(tweetBox, goBox) ||
        !bytesEqual(tweetOpen(key, nonce, goBox), plain)) {
      throw StateError('TweetNaCl and Go disagree at $size bytes');
    }
    add(size, 'tweetnacl seal', median(() => tweetSeal(key, nonce, plain)));
    add(size, 'go seal', median(() => quic.secretboxSeal(key, nonce, plain)));
    add(size, 'tweetnacl open', median(() => tweetOpen(key, nonce, tweetBox)));
    add(size, 'go open', median(() => quic.secretboxOpen(key, nonce, goBox)));

    // 一条 JSON 消息：发送从 Map 到上线的字节，接收从收到的字节到 Map
    final msg = message(size);
    final legacy = session();
    final legacyFrame = _deskSend(msg, legacy.recvKey, legacy.id);
    final now = session();
    final desk = now.asPhone();
    final frame = desk.seal(
      utf8.encode(jsonEncode(msg)),
      direction: dirDesktopToMobile,
      kind: kindText,
    );
    add(size, 'legacy send', median(() => legacySend(legacy, msg)));
    add(
      size,
      'send',
      median(
        () => now.seal(
          utf8.encode(jsonEncode(msg)),
          direction: dirMobileToDesktop,
          kind: kindText,
        ),
      ),
    );
    add(
      size,
      'legacy recv',
      median(() {
        legacy.recvN = 0;
        legacyRecv(legacy, legacyFrame);
      }),
    );
    add(
      size,
      'recv',
      median(() {
        now.recvN = 0;
        final (_, p) = now.openMessage(
          frame,
          binary: true,
          direction: dirDesktopToMobile,
        );
        _jsonUtf8.convert(p);
      }),
    );
  }
  return rows;
}

// 电脑发给手机的第 0 帧（分帧 2），用来量 legacy recv。
String _deskSend(Map<String, Object?> msg, Uint8List key, Uint8List id) {
  final nonce = frameNonce(id, dirDesktopToMobile, kindText, 0);
  final plain = concat([
    frameHeader(id, dirDesktopToMobile, kindText, 0),
    utf8.encode(jsonEncode(msg)),
  ]);
  return base64.encode(concat([nonce, tweetSeal(key, nonce, plain)]));
}

/// 每种大小下，legacy 和现在的耗时之比。
List<String> speedups(List<BenchRow> rows) => [
  for (final size in sizes)
    for (final (old, now) in [
      ('tweetnacl seal', 'go seal'),
      ('tweetnacl open', 'go open'),
      ('legacy send', 'send'),
      ('legacy recv', 'recv'),
    ])
      '${'$size'.padLeft(8)}  ${'$old → $now'.padRight(32)}'
          '${(_of(rows, size, old) / _of(rows, size, now)).toStringAsFixed(1)}×',
];

double _of(List<BenchRow> rows, int size, String op) =>
    rows.firstWhere((r) => r.size == size && r.op == op).micros;

void main() {
  stdout.writeln('${Platform.operatingSystem} ${Platform.version}');
  // JIT 要先把各条路径都跑热，不然先量的几项（300 B 的 seal）偏慢；手机上是 AOT，不用
  runE2eeBench();
  final rows = runE2eeBench(stdout.writeln);
  speedups(rows).forEach(stdout.writeln);
}
