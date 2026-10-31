import 'package:flutter/material.dart';
import 'package:file_selector/file_selector.dart';
import 'package:flutter/services.dart';
import 'package:image_picker/image_picker.dart';

import '../ui/theme.dart';
import '../ui/widgets.dart';

/// 对话一次最多带这么多张，避免一串大图堵住连接。
const maxChatImages = 9;

const _maxSide = 2048.0;
const _quality = 85;

/// 对话页选图从哪来：相册（照片和视频）、相机、剪贴板、「文件」App。
enum ImageAsk { gallery, camera, clipboard, files }

/// 问从哪选图。取消时返回 null。
Future<ImageAsk?> askImageSource(BuildContext context) {
  return showModalBottomSheet<ImageAsk>(
    context: context,
    showDragHandle: true,
    builder: (sheet) {
      final c = PosseColors.of(sheet);
      return SafeArea(
        top: false,
        child: Section(
          inset: 48,
          children: [
            RowTile(
              leading: Icon(
                Icons.photo_rounded,
                size: Ic.button,
                color: c.accent,
              ),
              title: const Text('从相册选择照片或视频，可以多选'),
              onTap: () => Navigator.of(sheet).pop(ImageAsk.gallery),
            ),
            RowTile(
              leading: Icon(
                Icons.photo_camera_rounded,
                size: Ic.button,
                color: c.accent,
              ),
              title: const Text('拍照'),
              onTap: () => Navigator.of(sheet).pop(ImageAsk.camera),
            ),
            RowTile(
              leading: Icon(
                Icons.content_paste_rounded,
                size: Ic.button,
                color: c.accent,
              ),
              title: const Text('从剪贴板粘贴'),
              onTap: () => Navigator.of(sheet).pop(ImageAsk.clipboard),
            ),
            RowTile(
              leading: Icon(
                Icons.folder_rounded,
                size: Ic.button,
                color: c.accent,
              ),
              title: const Text('从「文件」选择'),
              onTap: () => Navigator.of(sheet).pop(ImageAsk.files),
            ),
          ],
        ),
      );
    },
  );
}

/// 相册或相机里的一张。取消时返回 null。最长边压到 2048。
Future<Uint8List?> pickOneImage(ImageSource source) async {
  final file = await _pick(
    () => ImagePicker().pickImage(
      source: source,
      maxWidth: _maxSide,
      maxHeight: _maxSide,
      imageQuality: _quality,
    ),
  );
  if (file == null) return null;
  return file.readAsBytes();
}

Future<T?> _pick<T>(Future<T?> Function() run) async {
  try {
    return await run();
  } on PlatformException catch (error) {
    final code = error.code;
    if (code.contains('denied') || code.contains('restricted')) {
      throw StateError('没有照片权限，在系统设置里允许 Posse 访问照片或相机');
    }
    rethrow;
  }
}

/// 快捷键栏「发送文件」从哪选：相册里的照片和视频、拍照、「文件」App。
enum FileAsk { gallery, camera, files }

/// 问从哪选文件。取消时返回 null。
Future<FileAsk?> askFileSource(BuildContext context) {
  return showModalBottomSheet<FileAsk>(
    context: context,
    showDragHandle: true,
    builder: (sheet) {
      final c = PosseColors.of(sheet);
      Widget row(FileAsk ask, IconData icon, String title) => RowTile(
        key: Key('file-ask-${ask.name}'),
        leading: Icon(icon, size: Ic.button, color: c.accent),
        title: Text(title),
        chevron: false,
        onTap: () => Navigator.of(sheet).pop(ask),
      );
      return SafeArea(
        top: false,
        child: Section(
          inset: 48,
          children: [
            row(FileAsk.gallery, Icons.photo_library_rounded, '从相册选择照片或视频'),
            row(FileAsk.camera, Icons.photo_camera_rounded, '拍照'),
            row(FileAsk.files, Icons.folder_rounded, '从「文件」选择'),
          ],
        ),
      );
    },
  );
}

/// 按 [ask] 选一个文件。照片照旧压到最长边 2048，视频和「文件」里的原样。取消时返回 null。
Future<XFile?> pickAskedFile(FileAsk ask) {
  return switch (ask) {
    FileAsk.gallery => _pick(
      () => ImagePicker().pickMedia(
        maxWidth: _maxSide,
        maxHeight: _maxSide,
        imageQuality: _quality,
      ),
    ),
    FileAsk.camera => _pick(
      () => ImagePicker().pickImage(
        source: ImageSource.camera,
        maxWidth: _maxSide,
        maxHeight: _maxSide,
        imageQuality: _quality,
      ),
    ),
    FileAsk.files => openFile(),
  };
}

/// 相册里一次多选照片和视频。取消时返回空列表。最多 [limit] 个。
Future<List<XFile>> pickGalleryMedia({int limit = maxChatImages}) async {
  if (limit < 1) return const [];
  final files = await _pick(
    () => ImagePicker().pickMultipleMedia(
      maxWidth: _maxSide,
      maxHeight: _maxSide,
      imageQuality: _quality,
      limit: limit,
    ),
  );
  return files == null ? const [] : files.take(limit).toList();
}

/// 按扩展名认图片：对话里图片先预览、发送时再传；别的（视频、文件）选了就传。
bool isImageFile(String name) {
  final dot = name.lastIndexOf('.');
  if (dot < 0) return false;
  return const {
    'jpg',
    'jpeg',
    'png',
    'gif',
    'webp',
    'heic',
    'heif',
  }.contains(name.substring(dot + 1).toLowerCase());
}
