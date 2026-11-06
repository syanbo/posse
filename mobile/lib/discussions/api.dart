import 'dart:convert';
import 'dart:async';
import 'dart:typed_data';

import 'package:file_selector/file_selector.dart';
import 'package:crypto/crypto.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../app_state.dart';
import '../protocol/rpc.dart';
import 'models.dart';

class DiscussionApi {
  DiscussionApi(this.model) : computer = model.activeId;
  final PosseAppModel model;
  final String? computer;
  bool get connected =>
      computer != null && computer == model.activeId && model.rpc != null;

  Future<DiscJson> call(String method, DiscJson params) async {
    final rpc = model.rpc;
    if (!connected || rpc == null) {
      throw RpcException('closed', 'connection closed');
    }
    final result = await rpc.call(
      'discussion.$method',
      params,
      timeout: Duration(minutes: method == 'post' ? 11 : 2),
    );
    if (computer != model.activeId) {
      throw RpcException('closed', 'connection closed');
    }
    return result;
  }

  Future<DiscJson> upload(XFile file, void Function(double) progress) async {
    final size = await file.length();
    if (size > 20 * 1024 * 1024) {
      throw RpcException('discussion_file_large', '');
    }
    var name = file.name;
    if (name.toLowerCase().endsWith('.png') && size >= 3) {
      final header = await file
          .openRead(0, 3)
          .expand((chunk) => chunk)
          .toList();
      // Android 相册压缩可能生成 JPEG，却保留原 PNG 文件名。
      if (header[0] == 0xff && header[1] == 0xd8 && header[2] == 0xff) {
        name = '${name.substring(0, name.length - 4)}.jpg';
      }
    }
    final upload = await call('upload.begin', {'name': name, 'size': size});
    final id = discText(upload, 'staged');
    try {
      var offset = 0;
      await for (final chunk in file.openRead()) {
        for (var start = 0; start < chunk.length; start += 256 * 1024) {
          final end = (start + 256 * 1024).clamp(0, chunk.length);
          final part = chunk.sublist(start, end);
          final params = {
            'staged': id,
            'offset': offset,
            'content': base64Encode(part),
          };
          try {
            await call('upload.chunk', params);
          } on TimeoutException {
            if (!connected) rethrow;
            await call('upload.chunk', params);
          }
          offset += part.length;
          progress(size == 0 ? 1 : offset / size);
        }
      }
      final result = await call('upload.commit', {'staged': id});
      result['sha'] = (await sha256.bind(file.openRead()).first).toString();
      return result;
    } catch (_) {
      try {
        await call('upload.abort', {'staged': id});
      } catch (_) {}
      rethrow;
    }
  }

  Future<Uint8List> download(
    String room,
    String message,
    String attachment, {
    bool report = false,
  }) async {
    final bytes = BytesBuilder(copy: false);
    while (true) {
      final result = await call(report ? 'report.read' : 'attachment.read', {
        'discussion': room,
        'message': message,
        'attachment': attachment,
        'offset': bytes.length,
      });
      final part = rpcBytes(result['content']);
      bytes.add(part);
      if (bytes.length >= discInt(result, 'size')) return bytes.takeBytes();
      if (part.isEmpty || bytes.length > 20 * 1024 * 1024) {
        throw RpcException('discussion_download', '');
      }
    }
  }
}

/// 手机的草稿和已读独立保存，不修改桌面窗口的 ui.json。
class DiscussionStorage {
  DiscussionStorage(this.computer, this.room);
  final String computer;
  final String room;
  String get key =>
      'posse.discussion.${base64UrlEncode(utf8.encode('$computer/$room'))}';
  Future<DiscJson> read() async {
    final prefs = await SharedPreferences.getInstance();
    try {
      return discMap(jsonDecode(prefs.getString(key) ?? '{}'));
    } catch (_) {
      return {};
    }
  }

  Future<void> write(DiscJson value) async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.setString(key, jsonEncode(value));
  }
}

class DiscThumb {
  DiscThumb(this.bytes, this.width, this.height);
  final Uint8List bytes;
  final int width, height;
}

/// 讨论页里图片附件的缩略图：只留最近的几十张，同一张只要一次。
/// 旧电脑没有缩略图接口、图片还没同步到、解不开时给 null，消息里换成文件条。
class DiscThumbs {
  DiscThumbs(this.api);
  final DiscussionApi api;
  static const keep = 40;
  final _cache = <String, Future<DiscThumb?>>{};

  Future<DiscThumb?> get(String room, String message, String attachment) {
    final key = '$message/$attachment';
    final hit = _cache.remove(key);
    if (hit != null) return _cache[key] = hit;
    final next = _load(room, message, attachment);
    _cache[key] = next;
    while (_cache.length > keep) {
      _cache.remove(_cache.keys.first);
    }
    // 一时没取到（断线、还没同步）的不留，下次进来再试
    next.then((v) {
      if (v == null) _cache.remove(key);
    });
    return next;
  }

  Future<DiscThumb?> _load(
    String room,
    String message,
    String attachment,
  ) async {
    try {
      final r = await api.call('attachment.thumb', {
        'discussion': room,
        'message': message,
        'attachment': attachment,
      });
      final bytes = rpcBytes(r['content']);
      if (bytes.isEmpty) return null;
      return DiscThumb(bytes, discInt(r, 'width'), discInt(r, 'height'));
    } catch (_) {
      return null;
    }
  }
}
