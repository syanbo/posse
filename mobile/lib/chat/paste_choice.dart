import 'dart:typed_data';

/// 剪贴板里认得出的内容。文字和图片不会同时有：有文字就用文字。
class PasteContent {
  const PasteContent({this.text, this.image});

  final String? text;
  final Uint8List? image;

  bool get isText => text != null && text!.isNotEmpty;
  bool get isImage => image != null && image!.isNotEmpty;
}

/// 只认文字和图片。文字优先，避免复制文件时带上的文件名被当成图片。两边都没有就是空的。
PasteContent choosePaste(String? text, Uint8List? image) {
  if (text != null && text.isNotEmpty) return PasteContent(text: text);
  if (image != null && image.isNotEmpty) return PasteContent(image: image);
  return const PasteContent();
}
