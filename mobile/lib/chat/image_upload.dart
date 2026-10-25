import 'dart:convert';
import 'dart:math';

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
  if (bytes.isEmpty) throw StateError('图片是空的');
  final encoded = base64.encode(bytes);
  if (encoded.length <= imageChunkChars) {
    final path = imagePath(
      await rpc.call('clipboard.saveImageAsTempFile', {
        'contentBase64': encoded,
      }),
    );
    onProgress?.call(1);
    return path;
  }
  // 新电脑认 expectedBytes 并回一个编号：分段走二进制帧，原始字节、不等每段的回复；
  // 旧电脑只认 expectedBase64Length，照旧一段段传 base64
  final start = await rpc.call('clipboard.startImageUpload', {
    'expectedBase64Length': encoded.length,
    'expectedBytes': bytes.length,
  });
  final id = start['uploadId'];
  if (id is! String || id.isEmpty) throw StateError('电脑没有接受这张图片');
  final blob = start['blob'];
  if (blob is num) {
    for (var off = 0; off < bytes.length; off += bulkChunk) {
      final end = min(off + bulkChunk, bytes.length);
      await rpc.sendUploadChunk(blob.toInt(), bytes.sublist(off, end));
      onProgress?.call(min(end / bytes.length, 0.99));
    }
  } else {
    await _uploadBase64(rpc, id, encoded, onProgress);
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
  throw StateError('电脑没有保存这张图片');
}
