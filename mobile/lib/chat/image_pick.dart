import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:image_picker/image_picker.dart';

import '../ui/theme.dart';
import '../ui/widgets.dart';

/// 对话一次最多带这么多张，避免一串大图堵住连接。
const maxChatImages = 9;

const _maxSide = 2048.0;
const _quality = 85;

/// 选图从哪来。终端只用相册和相机；对话多一个剪贴板。
enum ImageAsk { gallery, camera, clipboard }

/// 问从哪选图。取消时返回 null。
Future<ImageAsk?> askImageSource(
  BuildContext context, {
  bool clipboard = false,
}) {
  return showModalBottomSheet<ImageAsk>(
    context: context,
    showDragHandle: true,
    builder: (sheet) {
      final c = HiveColors.of(sheet);
      return SafeArea(
        top: false,
        child: Section(
          title: '发送图片',
          inset: 48,
          children: [
            RowTile(
              leading: Icon(Icons.photo_outlined, size: 22, color: c.accent),
              title: Text(clipboard ? '从相册选择，可以多张' : '从相册选择'),
              onTap: () => Navigator.of(sheet).pop(ImageAsk.gallery),
            ),
            RowTile(
              leading: Icon(
                Icons.photo_camera_outlined,
                size: 22,
                color: c.accent,
              ),
              title: const Text('拍照'),
              onTap: () => Navigator.of(sheet).pop(ImageAsk.camera),
            ),
            if (clipboard)
              RowTile(
                leading: Icon(
                  Icons.content_paste_rounded,
                  size: 22,
                  color: c.accent,
                ),
                title: const Text('从剪贴板粘贴'),
                onTap: () => Navigator.of(sheet).pop(ImageAsk.clipboard),
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

/// 终端只发一张。剪贴板不从这里走。
Future<Uint8List?> pickAskedImage(ImageAsk ask) {
  return switch (ask) {
    ImageAsk.gallery => pickOneImage(ImageSource.gallery),
    ImageAsk.camera => pickOneImage(ImageSource.camera),
    ImageAsk.clipboard => Future<Uint8List?>.value(),
  };
}

/// 相册里一次多张。取消时返回空列表。最多 [limit] 张。
Future<List<Uint8List>> pickGalleryImages({int limit = maxChatImages}) async {
  if (limit < 1) return const [];
  final files = await _pick(
    () => ImagePicker().pickMultiImage(
      maxWidth: _maxSide,
      maxHeight: _maxSide,
      imageQuality: _quality,
      limit: limit,
    ),
  );
  if (files == null || files.isEmpty) return const [];
  final out = <Uint8List>[];
  for (final file in files.take(limit)) {
    out.add(await file.readAsBytes());
  }
  return out;
}

Future<T?> _pick<T>(Future<T?> Function() run) async {
  try {
    return await run();
  } on PlatformException catch (error) {
    final code = error.code;
    if (code.contains('denied') || code.contains('restricted')) {
      throw StateError('没有照片权限，在系统设置里允许 Hive 访问照片或相机');
    }
    rethrow;
  }
}
