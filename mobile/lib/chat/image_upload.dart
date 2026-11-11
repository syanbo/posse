import 'dart:async';
import 'dart:convert';
import 'dart:math';

import 'package:image_picker/image_picker.dart' show XFile;

import '../app_state.dart';
import '../protocol/rpc.dart';

/// 单块 base64 的字符数。整段 JSON 要低于服务端 WebSocket 的 4MB 读取上限；
/// 块小一些进度才走得匀，4G 上一张几 MB 的照片也能看出在动。
const imageChunkChars = 256 * 1024;

/// 上传进度：已经传到电脑上的比例，0 到 1。
typedef UploadProgress = void Function(double done);

/// 说明在前，每张图的路径各占一行。没有说明时只发路径，电脑上的 agent 按这些文件读图。
String imageMessage(String caption, List<String> paths) {
  final text = caption.trim();
  final lines = paths.where((path) => path.isNotEmpty).join('\n');
  if (text.isEmpty) return lines;
  if (lines.isEmpty) return text;
  return '$text\n$lines';
}

/// 把 base64 切成服务端 append 用的 (offset, chunk)。offset 是已发送的字符数。
List<(int, String)> imageChunks(String encoded) {
  final out = <(int, String)>[];
  for (var off = 0; off < encoded.length; off += imageChunkChars) {
    final end = min(off + imageChunkChars, encoded.length);
    out.add((off, encoded.substring(off, end)));
  }
  return out;
}

/// 在当前连接上跑一次上传：开始时才取连接（[current]，一般是 model.rpc），不用选文件、等确认之前拿到的那条。
/// 开始时撞上连接正在换下来（[UploadRefused]）就在换上来的连接上重来一次；已经传了一部分之后断开的照常报错，不自动重传。
Future<String> uploadOnCurrent(
  PhoneRpc? Function() current,
  Future<String> Function(PhoneRpc rpc) run,
) async {
  for (var attempt = 0; ; attempt++) {
    final rpc = current();
    if (rpc == null) throw RpcException('closed', 'connection closed');
    try {
      return await run(rpc);
    } on UploadRefused {
      if (attempt > 0) rethrow;
    }
  }
}

/// 把图片存到电脑的临时目录，返回路径。小图一次传完，大图按块传，每传完一块报一次进度。
/// 整个过程记在这条连接的上传数里（[PhoneRpc.upload]）：换连接时旧连接等它传完再关。
Future<String> uploadImage(
  PhoneRpc rpc,
  List<int> bytes, {
  UploadProgress? onProgress,
}) => rpc.upload(() => _uploadImage(rpc, bytes, onProgress: onProgress));

Future<String> _uploadImage(
  PhoneRpc rpc,
  List<int> bytes, {
  UploadProgress? onProgress,
}) async {
  if (bytes.isEmpty) throw const PhoneError(PhoneError.imageEmpty);
  // 新电脑走二进制分段，用不到 base64：只在一次传完和旧电脑时才编码
  final base64Length = (bytes.length + 2) ~/ 3 * 4;
  if (base64Length <= imageChunkChars) {
    final path = imagePath(
      await rpc.call('clipboard.saveImageAsTempFile', {
        'contentBase64': base64.encode(bytes),
      }),
    );
    onProgress?.call(1);
    return path;
  }
  // 新电脑认 expectedBytes 并回一个编号：分段走二进制帧，原始字节、不等每段的回复；
  // 旧电脑只认 expectedBase64Length，照旧一段段传 base64
  final start = await rpc.call('clipboard.startImageUpload', {
    'expectedBase64Length': base64Length,
    'expectedBytes': bytes.length,
  });
  final id = start['uploadId'];
  if (id is! String || id.isEmpty) {
    throw const PhoneError(PhoneError.imageNotAccepted);
  }
  final blob = start['blob'];
  if (blob is num) {
    final sender = UploadSender(rpc, blob.toInt(), acked: start['ack'] == true);
    try {
      for (var off = 0; off < bytes.length; off += bulkChunk) {
        final end = min(off + bulkChunk, bytes.length);
        await sender.send(bytes.sublist(off, end));
        onProgress?.call(min(end / bytes.length, 0.99));
      }
    } catch (_) {
      unawaited(_abortUpload(rpc, id));
      rethrow;
    } finally {
      sender.close();
    }
  } else {
    await _uploadBase64(rpc, id, base64.encode(bytes), onProgress);
  }
  final path = imagePath(
    await rpc.call('clipboard.commitImageUpload', {'uploadId': id}),
  );
  onProgress?.call(1);
  return path;
}

/// 按电脑的确认控制上传在路上的字节数。不控制的话整张图一下子进了发送队列：同一连接上的按键、ping
/// 排在它后面，手机上行占满还拖慢看桌面的输入和接收报告，延迟一下子飙上去。
///
/// 窗口按往返时间调：比最小往返多出 [queueLimit] 说明在排队，缩到四分之三（每个往返最多缩一次）；
/// 否则每次确认加一段。电脑不回确认（旧版）时照旧一口气发。
///
/// 等确认时连接断了马上报 closed；确认 [stallTimeout] 没前进、期间也没收到任何数据才算超时：
/// 确认和电脑发来的大回复共用一条连接，慢线路上可能排在后面，连接还在收东西就接着等，最多 [maxStall]。
class UploadSender {
  UploadSender(this.rpc, this.blob, {required this.acked}) {
    if (acked) _cancel = rpc.watchUploadAcks(blob, _onAck, _onClosed);
  }

  static const minWindow = bulkChunk;
  static const maxWindow = 4 << 20;
  static const queueLimit = Duration(milliseconds: 60);
  static const stallTimeout = Duration(seconds: 20);
  static const maxStall = Duration(minutes: 2);

  final PhoneRpc rpc;
  final int blob;

  /// 电脑会回确认（startImageUpload 的回复带 ack）
  final bool acked;
  void Function()? _cancel;

  var window = 4 * bulkChunk;
  var _sent = 0;
  var _got = 0;
  var _closed = false;
  Duration? _minRtt;
  var _lastCut = DateTime.fromMillisecondsSinceEpoch(0);
  var _progressAt = DateTime.now();
  // 每段发完时的累计字节和时刻，按确认算往返
  final _marks = <(int, DateTime)>[];
  Completer<void>? _wake;

  int get inFlight => _sent - _got;

  Future<void> send(List<int> chunk) async {
    if (acked) await _waitForRoom(chunk.length);
    await rpc.sendUploadChunk(blob, chunk);
    _sent += chunk.length;
    if (acked) _marks.add((_sent, DateTime.now()));
  }

  Future<void> _waitForRoom(int size) async {
    _progressAt = DateTime.now();
    var received = rpc.receivedBytes;
    var quietSince = DateTime.now();
    while (inFlight > 0 && inFlight + size > window) {
      if (_closed) throw RpcException('closed', 'connection closed');
      final wake = _wake = Completer<void>();
      try {
        await wake.future.timeout(const Duration(seconds: 1));
      } on TimeoutException {
        final now = DateTime.now();
        if (rpc.receivedBytes != received) {
          received = rpc.receivedBytes;
          quietSince = now;
        }
        final stalled = now.difference(_progressAt);
        if (stalled > maxStall ||
            (stalled > stallTimeout &&
                now.difference(quietSince) > stallTimeout)) {
          throw TimeoutException('upload ack');
        }
      }
    }
    if (_closed) throw RpcException('closed', 'connection closed');
  }

  void close() {
    _cancel?.call();
    _cancel = null;
  }

  void _onAck(int got) => ack(got, DateTime.now());

  void _onClosed() {
    _closed = true;
    _signal();
  }

  void _signal() {
    final wake = _wake;
    _wake = null;
    if (wake != null && !wake.isCompleted) wake.complete();
  }

  /// 收到累计 [got] 字节的确认。测试直接调。
  void ack(int got, DateTime now) {
    if (got <= _got) return;
    _got = got;
    _progressAt = now;
    DateTime? sentAt;
    while (_marks.isNotEmpty && _marks.first.$1 <= got) {
      sentAt = _marks.removeAt(0).$2;
    }
    if (sentAt != null) {
      final rtt = now.difference(sentAt);
      final base = _minRtt == null || rtt < _minRtt!
          ? (_minRtt = rtt)
          : _minRtt!;
      if (rtt - base > queueLimit) {
        if (now.difference(_lastCut) > rtt) {
          window = max(minWindow, window * 3 ~/ 4);
          _lastCut = now;
        }
      } else {
        window = min(maxWindow, window + bulkChunk);
      }
    }
    _signal();
  }
}

/// 分段上传出错时告诉电脑丢掉没传完的临时文件；连接断了就算了，断开时电脑自己清。
Future<void> _abortUpload(PhoneRpc rpc, String id) async {
  try {
    await rpc.call('clipboard.abortImageUpload', {
      'uploadId': id,
    }, timeout: const Duration(seconds: 5));
  } catch (_) {}
}

Future<void> _uploadBase64(
  PhoneRpc rpc,
  String id,
  String encoded,
  UploadProgress? onProgress,
) async {
  for (final (off, chunk) in imageChunks(encoded)) {
    await rpc.call('clipboard.appendImageUploadChunk', {
      'uploadId': id,
      'offset': off,
      'contentBase64': chunk,
    });
    // 落盘还要一步，传完最后一块先停在 99%
    onProgress?.call(min((off + chunk.length) / encoded.length, 0.99));
  }
}

/// save 和 commit 把路径放在 value 里（结果是字符串，不是对象）。
String imagePath(Map<String, Object?> result) {
  final value = result['value'];
  if (value is String && value.isNotEmpty) return value;
  throw const PhoneError(PhoneError.imageNotSaved);
}

/// base64 上传整段攒在电脑内存里，只能这么大；视频、大文件要电脑认二进制帧上传（posse.bulk.v1）
const _maxBase64Upload = 32 << 20;

/// 把手机上的一个文件（照片、视频、「文件」里的）存到电脑上，返回路径。带上文件名，电脑用原名保存；
/// 从文件流式读、按二进制帧发，几百 MB 的视频也不整个读进内存。换连接时旧连接等它传完再关（[PhoneRpc.upload]）。
Future<String> uploadFile(
  PhoneRpc rpc,
  XFile file, {
  UploadProgress? onProgress,
}) => rpc.upload(() => _uploadFile(rpc, file, onProgress: onProgress));

Future<String> _uploadFile(
  PhoneRpc rpc,
  XFile file, {
  UploadProgress? onProgress,
}) async {
  final size = await file.length();
  if (size == 0) throw const PhoneError(PhoneError.fileEmpty);
  final start = await rpc.call('clipboard.startImageUpload', {
    'expectedBase64Length': (size + 2) ~/ 3 * 4,
    'expectedBytes': size,
    'name': uploadName(file.name),
  });
  final id = start['uploadId'];
  if (id is! String || id.isEmpty) {
    throw const PhoneError(PhoneError.fileNotAccepted);
  }
  final blob = start['blob'];
  if (blob is num) {
    final sender = UploadSender(rpc, blob.toInt(), acked: start['ack'] == true);
    var sent = 0;
    var buf = <int>[];
    Future<void> flush() async {
      await sender.send(buf);
      sent += buf.length;
      buf = <int>[];
      onProgress?.call(min(sent / size, 0.99));
    }

    try {
      await for (final piece in file.openRead()) {
        var off = 0;
        while (off < piece.length) {
          final take = min(bulkChunk - buf.length, piece.length - off);
          buf.addAll(piece.sublist(off, off + take));
          off += take;
          if (buf.length == bulkChunk) await flush();
        }
      }
      if (buf.isNotEmpty) await flush();
    } catch (_) {
      unawaited(_abortUpload(rpc, id));
      rethrow;
    } finally {
      sender.close();
    }
  } else {
    // 旧电脑：只能整段 base64，也不认文件名
    if (size > _maxBase64Upload) {
      await rpc.call('clipboard.abortImageUpload', {'uploadId': id});
      throw const PhoneError(PhoneError.fileTooLargeForOldPosse);
    }
    await _uploadBase64(
      rpc,
      id,
      base64.encode(await file.readAsBytes()),
      onProgress,
    );
  }
  final path = imagePath(
    await rpc.call('clipboard.commitImageUpload', {'uploadId': id}),
  );
  onProgress?.call(1);
  return path;
}

/// image_picker 把相册里的视频拷到临时目录时，在原名前面加 `image_picker_` 和 globallyUniqueString
/// （UUID-进程号-16 位十六进制）；照片没有原名，只剩扩展名，保持不动。
final _pickerPrefix = RegExp(
  r'^image_picker_[0-9A-F]{8}(?:-[0-9A-F]{4}){3}-[0-9A-F]{12}-\d+-[0-9A-F]{16}',
);

/// 电脑上保存用的文件名：去掉 image_picker 加的前缀，还原相册里的原名。
String uploadName(String name) {
  final m = _pickerPrefix.firstMatch(name);
  if (m == null) return name;
  final rest = name.substring(m.end);
  if (rest.isEmpty || rest.startsWith('.')) return name;
  return rest;
}

/// 把路径打进终端时，空格、括号这类 shell 会拆开或特殊处理的字符前面加反斜杠，和把文件拖进 macOS 终端一样；
/// Claude Code、Codex 照样认出是文件。中文这些非 ASCII 字符不用转义。
String shellPath(String path) {
  final out = StringBuffer();
  for (final rune in path.runes) {
    final ch = String.fromCharCode(rune);
    final safe = rune > 0x7f || RegExp(r'[A-Za-z0-9_./+\-@%:,=]').hasMatch(ch);
    if (!safe) out.write('\\');
    out.write(ch);
  }
  return out.toString();
}
