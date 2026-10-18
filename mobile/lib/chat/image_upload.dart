import 'dart:convert';
import 'dart:math';

import '../protocol/rpc.dart';

/// 单块 base64 的字符数。整段 JSON 要低于服务端 WebSocket 的 4MB 读取上限。
const imageChunkChars = 512 * 1024;

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

/// 把图片存到电脑的临时目录，返回路径。小图一次传完，大图按块传。
Future<String> uploadImage(PhoneRpc rpc, List<int> bytes) async {
  if (bytes.isEmpty) throw StateError('图片是空的');
  final encoded = base64.encode(bytes);
  if (encoded.length <= imageChunkChars) {
    return imagePath(
      await rpc.call('clipboard.saveImageAsTempFile', {
        'contentBase64': encoded,
      }),
    );
  }
  final start = await rpc.call('clipboard.startImageUpload', {
    'expectedBase64Length': encoded.length,
  });
  final id = start['uploadId'];
  if (id is! String || id.isEmpty) throw StateError('电脑没有接受这张图片');
  for (final (off, chunk) in imageChunks(encoded)) {
    await rpc.call('clipboard.appendImageUploadChunk', {
      'uploadId': id,
      'offset': off,
      'contentBase64': chunk,
    });
  }
  return imagePath(
    await rpc.call('clipboard.commitImageUpload', {'uploadId': id}),
  );
}

/// save 和 commit 把路径放在 value 里（结果是字符串，不是对象）。
String imagePath(Map<String, Object?> result) {
  final value = result['value'];
  if (value is String && value.isNotEmpty) return value;
  throw StateError('电脑没有保存这张图片');
}
