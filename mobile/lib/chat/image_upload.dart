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

/// 把图片存到电脑的临时目录，返回路径。小图一次传完，大图按块传，每传完一块报一次进度。
Future<String> uploadImage(
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
    for (var off = 0; off < bytes.length; off += bulkChunk) {
      final end = min(off + bulkChunk, bytes.length);
      await rpc.sendUploadChunk(blob.toInt(), bytes.sublist(off, end));
      onProgress?.call(min(end / bytes.length, 0.99));
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
/// 从文件流式读、按二进制帧发，几百 MB 的视频也不整个读进内存。
Future<String> uploadFile(
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
    var sent = 0;
    var buf = <int>[];
    Future<void> flush() async {
      await rpc.sendUploadChunk(blob.toInt(), buf);
      sent += buf.length;
      buf = <int>[];
      onProgress?.call(min(sent / size, 0.99));
    }

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
