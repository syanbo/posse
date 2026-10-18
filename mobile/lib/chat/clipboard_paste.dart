import 'package:pasteboard/pasteboard.dart';

import 'paste_choice.dart';

/// 读剪贴板。文件和其他类型不读：没有文字时才看图片。
Future<PasteContent> readClipboardPaste() async {
  final text = await Pasteboard.text;
  if (text != null && text.isNotEmpty) return choosePaste(text, null);
  return choosePaste(null, await Pasteboard.image);
}
