import 'dart:async';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:gal/gal.dart';
import 'package:share_plus/share_plus.dart';

import '../app_state.dart';
import '../protocol/rpc.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'video.dart';

/// 存进相册时放的相簿。
const album = 'Posse';

/// 两边相册都收的格式；其余的（webm、bmp、文本等）安卓存进"下载/Posse"，iOS 走系统分享面板（里面有"存储到文件"）。
const _albumImages = {'jpg', 'jpeg', 'png', 'gif', 'webp', 'heic', 'heif'};
const _albumVideos = {'mp4', 'mov', 'm4v'};

String _extOf(String name) {
  final dot = name.lastIndexOf('.');
  return dot < 0 ? '' : name.substring(dot + 1).toLowerCase();
}

/// [image] 是已知为图片、名字里可能没有扩展名的（会话里粘贴的图片），存的时候按内容补扩展名。
bool savesToAlbum(String name, {bool image = false}) {
  final ext = _extOf(name);
  return image && ext.isEmpty ||
      _albumImages.contains(ext) ||
      _albumVideos.contains(ext);
}

/// 存到哪：相册的 Posse 相簿、安卓的"下载/Posse"、系统分享面板。
/// 安卓的分享面板里没有存到本机的去处（小米、华为的"文件管理"都是存到云空间），所以安卓直接存进下载目录。
enum SaveTo { album, downloads, share }

SaveTo saveTarget(String name, {bool image = false}) =>
    savesToAlbum(name, image: image)
    ? SaveTo.album
    : (Platform.isAndroid ? SaveTo.downloads : SaveTo.share);

IconData saveIcon(SaveTo to) => to != SaveTo.share
    ? Icons.download_rounded
    : (Platform.isIOS ? Icons.ios_share_rounded : Icons.share_outlined);

String saveLabel(SaveTo to) => switch (to) {
  SaveTo.album => '存到相册',
  SaveTo.downloads => '存到下载',
  SaveTo.share => '分享',
};

/// 安卓存进"下载/Posse"（MainActivity.kt）。
const _downloads = MethodChannel('posse/downloads');

/// 会话里粘贴的图片没有扩展名，按文件头认；认不出返回空串。
String sniffImageExt(Uint8List head) {
  bool at(int offset, List<int> sig) {
    if (head.length < offset + sig.length) return false;
    for (var i = 0; i < sig.length; i++) {
      if (head[offset + i] != sig[i]) return false;
    }
    return true;
  }

  if (at(0, const [0x89, 0x50, 0x4e, 0x47])) return 'png';
  if (at(0, const [0xff, 0xd8, 0xff])) return 'jpg';
  if (at(0, const [0x47, 0x49, 0x46, 0x38])) return 'gif';
  if (at(0, const [0x52, 0x49, 0x46, 0x46]) &&
      at(8, const [0x57, 0x45, 0x42, 0x50])) {
    return 'webp';
  }
  return '';
}

/// 存出去的文件名：[name] 的最后一段；没有扩展名时按内容补上，[name] 不像文件名时用时间起名。
Future<String> _fileName(String name, File file) async {
  var base = name.split('/').last.trim();
  if (_extOf(base).isNotEmpty) return base;
  final head = await file
      .openRead(0, 16)
      .fold<List<int>>([], (all, part) => all..addAll(part));
  final ext = sniffImageExt(Uint8List.fromList(head));
  if (ext.isEmpty) return base.isEmpty ? 'Posse' : base;
  if (base.isEmpty || base.contains(' ') || base == '粘贴') {
    final t = DateTime.now();
    String two(int v) => v.toString().padLeft(2, '0');
    base =
        'Posse-${t.year}${two(t.month)}${two(t.day)}-'
        '${two(t.hour)}${two(t.minute)}${two(t.second)}';
  }
  return '$base.$ext';
}

/// 把手机上的 [file] 以 [name] 存到 [to]，不给时按格式定（[saveTarget]）；相册拒收的格式改存下载或分享。
/// 缓存里的文件名是哈希，先复制一份改成原名：相册、下载目录和分享面板都按文件名认格式、显示名字。
Future<void> saveToPhone(
  BuildContext context,
  File file,
  String name, {
  SaveTo? to,
}) async {
  final messenger = ScaffoldMessenger.of(context);
  final box = context.findRenderObject() as RenderBox?;
  final origin = box == null || !box.hasSize
      ? null
      : box.localToGlobal(Offset.zero) & box.size;
  try {
    final named = await _named(file, await _fileName(name, file));
    var target = to ?? saveTarget(named.path);
    if (target == SaveTo.album) {
      // 放进单独的相簿要读写权限（只给"仅添加"建不了相簿）；用户选"限制访问"也够
      if (!await Gal.requestAccess(toAlbum: true)) {
        messenger.showSnackBar(const SnackBar(content: Text(_denied)));
        return;
      }
      try {
        if (_albumVideos.contains(_extOf(named.path))) {
          await Gal.putVideo(named.path, album: album);
        } else {
          await Gal.putImage(named.path, album: album);
        }
        messenger.showSnackBar(const SnackBar(content: Text('已存到相册的「Posse」相簿')));
        return;
      } on GalException catch (e) {
        if (e.type != GalExceptionType.notSupportedFormat) rethrow;
        target = Platform.isAndroid ? SaveTo.downloads : SaveTo.share;
      }
    }
    if (target == SaveTo.downloads) {
      try {
        final saved = await _downloads.invokeMethod<String>('save', {
          'path': named.path,
          'name': named.uri.pathSegments.last,
        });
        messenger.showSnackBar(
          SnackBar(content: Text('已存到「下载/Posse」：${saved ?? ''}')),
        );
      } on PlatformException catch (e) {
        messenger.showSnackBar(
          SnackBar(
            content: Text(
              e.code == 'denied'
                  ? '没有存储权限：在系统设置里允许 Posse 访问存储'
                  : '没存进下载目录：${e.message ?? e.code}',
            ),
          ),
        );
      }
      return;
    }
    await SharePlus.instance.share(
      ShareParams(files: [XFile(named.path)], sharePositionOrigin: origin),
    );
  } on GalException catch (e) {
    messenger.showSnackBar(
      SnackBar(
        content: Text(switch (e.type) {
          GalExceptionType.accessDenied => _denied,
          GalExceptionType.notEnoughSpace => '手机空间不够，没存进相册',
          _ => '没存进相册：${e.platformException.message ?? e.type.code}',
        }),
      ),
    );
  } catch (e) {
    messenger.showSnackBar(SnackBar(content: Text(friendlyError(e))));
  }
}

const _denied = '没有相册权限：在系统设置里允许 Posse 访问照片';

/// 写成临时文件再存，用于已经在内存里的图片（文件页的图片预览）。
Future<void> saveBytesToPhone(
  BuildContext context,
  Uint8List bytes,
  String name,
) async {
  final dir = await _outDir();
  final file = File('${dir.path}/bytes');
  await file.writeAsBytes(bytes);
  if (!context.mounted) return;
  await saveToPhone(context, file, name);
}

/// 每次存之前清掉上次的副本：上次的分享面板已经关了，用不着了。
Future<Directory> _outDir() async {
  final dir = Directory('${Directory.systemTemp.path}/posse-save');
  if (await dir.exists()) await dir.delete(recursive: true);
  return dir.create(recursive: true);
}

Future<File> _named(File file, String name) async {
  if (file.parent.path.endsWith('/posse-save')) {
    return file.rename('${file.parent.path}/$name');
  }
  return file.copy('${(await _outDir()).path}/$name');
}

/// 还没在手机上的文件：弹框显示下载进度（可取消），下完存到 [to]（不给时按格式定）。
Future<void> downloadAndSave(
  BuildContext context, {
  required String name,
  required VideoFetcher fetch,
  SaveTo? to,
}) async {
  final file = await showPosseDialog<File>(
    context: context,
    builder: (_) => _DownloadDialog(name: name, fetch: fetch),
  );
  if (file == null || !context.mounted) return;
  await saveToPhone(context, file, name, to: to);
}

class _DownloadDialog extends StatefulWidget {
  const _DownloadDialog({required this.name, required this.fetch});

  final String name;
  final VideoFetcher fetch;

  @override
  State<_DownloadDialog> createState() => _DownloadDialogState();
}

class _DownloadDialogState extends State<_DownloadDialog> {
  var _cancelled = false;
  (int, int)? _progress;
  String? _error;

  @override
  void initState() {
    super.initState();
    _run();
  }

  Future<void> _run() async {
    setState(() => _error = null);
    try {
      final file = await widget.fetch(() => _cancelled, (done, total) {
        if (mounted) setState(() => _progress = (done, total));
      });
      if (mounted && !_cancelled) Navigator.of(context).pop(file);
    } on VideoCancelled {
      return;
    } on VideoTooLarge {
      _fail('文件超过 500 MB，不往手机上下载');
    } on RpcException catch (e) {
      _fail(
        e.code == 'closed'
            ? '和电脑的连接断了，下好的部分留着'
            : e.code == 'method_not_found'
            ? '电脑上的 posse 版本太旧，更新后才能下载'
            : friendlyError(e),
      );
    } on TimeoutException {
      _fail('网络太慢，没下完，下好的部分留着');
    } catch (e) {
      _fail(friendlyError(e));
    }
  }

  void _fail(String text) {
    if (mounted && !_cancelled) setState(() => _error = text);
  }

  @override
  void dispose() {
    _cancelled = true;
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final (done, total) = _progress ?? (0, 0);
    final error = _error;
    return AlertDialog(
      title: Text(
        widget.name.split('/').last,
        maxLines: 2,
        overflow: TextOverflow.ellipsis,
      ),
      content: error != null
          ? Text(error, style: TextStyle(color: c.danger))
          : Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                LinearProgressIndicator(
                  value: total == 0 ? null : done / total,
                  borderRadius: BorderRadius.circular(2),
                ),
                const SizedBox(height: 12),
                Text(
                  total == 0
                      ? '正在从电脑取文件…'
                      : '${formatBytes(done)} / ${formatBytes(total)}',
                  style: TextStyle(fontSize: Fs.meta, color: c.muted),
                ),
              ],
            ),
      actions: [
        TextButton(
          onPressed: () {
            _cancelled = true;
            Navigator.of(context).pop();
          },
          child: const Text('取消'),
        ),
        if (error != null)
          TextButton(onPressed: _run, child: const Text('接着下载')),
      ],
    );
  }
}

/// 长按文件、长按图片格子时弹出的面板：第一行按格式存（[to] 见 [saveTarget]），不是分享的再加一行分享。
Future<void> showSaveSheet(
  BuildContext context, {
  required SaveTo to,
  required Future<void> Function(SaveTo to) onSave,
}) async {
  final picked = await showModalBottomSheet<SaveTo>(
    context: context,
    showDragHandle: true,
    builder: (sheet) {
      final c = PosseColors.of(sheet);
      Widget row(SaveTo to, String subtitle) => RowTile(
        chevron: false,
        leading: Icon(saveIcon(to), size: 22, color: c.accent),
        title: Text(saveLabel(to)),
        subtitle: Text(subtitle),
        onTap: () => Navigator.of(sheet).pop(to),
      );
      return SafeArea(
        top: false,
        child: Section(
          inset: 48,
          children: [
            switch (to) {
              SaveTo.album => row(to, '下载到手机，放进相册的「Posse」相簿'),
              SaveTo.downloads => row(to, '下载到手机，放进「下载/Posse」，文件管理里能找到'),
              SaveTo.share => row(to, '下载到手机，用系统分享存到"文件"或发给别的 App'),
            },
            if (to != SaveTo.share) row(SaveTo.share, '下载到手机，发给别的 App'),
          ],
        ),
      );
    },
  );
  if (picked != null) await onSave(picked);
}
