import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:flutter_markdown_plus/flutter_markdown_plus.dart';
import 'package:markdown/markdown.dart' as md;
import 'package:url_launcher/url_launcher.dart';

import '../chat/markdown_view.dart';
import '../app_state.dart';
import '../terminal/link_target.dart';
import '../ui/theme.dart';

/// 文件页里渲染一个 Markdown 文件：表格、任务列表、嵌套列表、图片都画出来。
/// 相对链接和图片按这个文件所在的目录算，工作区里的文件在文件页打开、图片向电脑要。
class MarkdownDoc extends StatelessWidget {
  const MarkdownDoc({
    super.key,
    required this.data,
    required this.model,
    required this.worktree,
    required this.path,
    required this.openFile,
  });

  final String data;
  final HiveAppModel model;
  final String worktree;

  /// 这个 md 文件相对工作区根目录的路径。
  final String path;
  final void Function(String path) openFile;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final base = TextStyle(fontSize: Fs.read, height: 1.6, color: c.text);
    return MarkdownBody(
      data: data,
      extensionSet: markdownExtensions,
      styleSheet: markdownDocStyle(c, base),
      builders: {'pre': _CodeBlockBuilder(base)},
      paddingBuilders: {'hr': _RulePadding()},
      bulletBuilder: (p) => _bullet(p, base, c),
      checkboxBuilder: (checked) => Padding(
        padding: const EdgeInsets.only(right: 6),
        child: Icon(
          checked ? Icons.check_box_rounded : Icons.check_box_outline_blank,
          size: 18,
          color: checked ? c.accent : c.muted,
        ),
      ),
      imageBuilder: (uri, title, alt) => _image(uri, alt, c),
      onTapLink: (text, href, title) => _tapLink(context, href),
    );
  }

  Widget _bullet(MarkdownBulletParameters p, TextStyle base, HiveColors c) {
    if (p.style == BulletStyle.orderedList) {
      return Text(
        '${p.index + 1}.',
        textAlign: TextAlign.right,
        style: base.copyWith(color: c.muted),
      );
    }
    // 照 GitHub：一层实心圆、二层空心圆、再往下方块
    const marks = ['•', '◦', '▪'];
    return Text(
      marks[p.nestLevel.clamp(0, marks.length - 1)],
      textAlign: TextAlign.center,
      style: base.copyWith(color: c.muted),
    );
  }

  Widget _image(Uri uri, String? alt, HiveColors c) {
    if (uri.scheme == 'http' || uri.scheme == 'https') {
      return Image.network(
        uri.toString(),
        errorBuilder: (_, _, _) => _ImageAlt(alt, c),
      );
    }
    final rel = _relative(uri);
    if (rel == null) return _ImageAlt(alt, c);
    return _WorkspaceImage(
      model: model,
      worktree: worktree,
      path: rel,
      alt: alt,
    );
  }

  /// 相对这个文件的链接换成相对工作区根目录的路径；指到工作区外面或不是文件链接时为 null。
  String? _relative(Uri uri) {
    if (uri.hasScheme && uri.scheme != 'file') return null;
    final raw = Uri.decodeComponent(uri.path);
    if (raw.isEmpty || raw.startsWith('/')) return null;
    final slash = path.lastIndexOf('/');
    final dir = slash < 0 ? '' : path.substring(0, slash + 1);
    final target = resolveLink(filePath: '$dir$raw');
    return target is OpenFile ? target.path : null;
  }

  void _tapLink(BuildContext context, String? href) {
    if (href == null || href.isEmpty || href.startsWith('#')) return;
    final uri = Uri.tryParse(href);
    if (uri == null) return;
    final root = model.worktrees
        .where((w) => w.id == worktree)
        .firstOrNull
        ?.path;
    final LinkTarget? target;
    if (!uri.hasScheme && !uri.path.startsWith('/')) {
      final rel = _relative(uri);
      target = rel == null ? NotInWorkspace(href) : OpenFile(rel);
    } else {
      target = resolveLink(
        uri: uri.hasScheme ? uri : null,
        filePath: uri.path,
        root: root,
      );
    }
    final messenger = ScaffoldMessenger.of(context);
    void flash(String text) =>
        messenger.showSnackBar(SnackBar(content: Text(text)));
    switch (target) {
      case OpenUrl(:final uri):
        unawaited(() async {
          var ok = false;
          try {
            ok = await launchUrl(uri, mode: LaunchMode.externalApplication);
          } catch (_) {}
          if (!ok) flash('打不开这个链接：$uri');
        }());
      case OpenFile(:final path):
        openFile(path);
      case NotInWorkspace():
        flash('这个文件不在当前工作区里，手机上打不开');
      case null:
        break;
    }
  }
}

/// 文件预览的排版：标题拉开层级，块之间留足空隙，表格带边框、太宽时左右滑。
MarkdownStyleSheet markdownDocStyle(HiveColors c, TextStyle base) {
  TextStyle heading(double size) =>
      base.copyWith(fontSize: size, fontWeight: FontWeight.w600, height: 1.3);
  final rule = BorderSide(color: c.line);
  return MarkdownStyleSheet(
    p: base,
    a: base.copyWith(
      color: c.accent,
      decoration: TextDecoration.underline,
      decorationColor: c.accent.withValues(alpha: 0.4),
    ),
    code: base.copyWith(
      fontFamily: monoFamily,
      fontFamilyFallback: monoFallback,
      fontSize: Fs.read * 0.86,
      color: c.accent,
      backgroundColor: c.code,
    ),
    h1: heading(23),
    h1Padding: const EdgeInsets.only(top: 12),
    h2: heading(19.5),
    h2Padding: const EdgeInsets.only(top: 12),
    h3: heading(17),
    h3Padding: const EdgeInsets.only(top: 8),
    h4: heading(Fs.read),
    h4Padding: const EdgeInsets.only(top: 6),
    h5: heading(Fs.body).copyWith(color: c.muted),
    h6: heading(Fs.meta).copyWith(color: c.muted),
    strong: const TextStyle(fontWeight: FontWeight.w600),
    del: TextStyle(color: c.muted, decoration: TextDecoration.lineThrough),
    blockquote: base.copyWith(color: c.muted),
    img: base.copyWith(color: c.muted),
    checkbox: base,
    // 块间距对列表的每一项也生效，留小；段落、表格各自在下面补空隙
    blockSpacing: 4,
    pPadding: const EdgeInsets.only(bottom: 8),
    listIndent: 22,
    listBullet: base.copyWith(color: c.muted),
    listBulletPadding: const EdgeInsets.only(right: 6),
    tableHead: base.copyWith(fontSize: Fs.body, fontWeight: FontWeight.w600),
    tableBody: base.copyWith(fontSize: Fs.body, height: 1.45),
    tableHeadAlign: TextAlign.left,
    tablePadding: const EdgeInsets.only(bottom: 10),
    tableBorder: TableBorder.all(color: c.line),
    // 按内容宽排、超出屏幕左右滑（只有固定宽和内容宽两种才会套横向滚动）
    tableColumnWidth: const IntrinsicColumnWidth(),
    tableCellsPadding: const EdgeInsets.symmetric(horizontal: 10, vertical: 6),
    tableCellsDecoration: const BoxDecoration(),
    tableHeadCellsDecoration: BoxDecoration(color: c.code),
    blockquotePadding: const EdgeInsets.fromLTRB(12, 2, 0, 2),
    blockquoteDecoration: BoxDecoration(
      border: Border(left: BorderSide(color: c.line, width: 3)),
    ),
    // 代码块整个由 markdownCodeBlock 画，这里不再套一层
    codeblockPadding: EdgeInsets.zero,
    codeblockDecoration: const BoxDecoration(),
    horizontalRuleDecoration: BoxDecoration(border: Border(top: rule)),
  );
}

class _CodeBlockBuilder extends MarkdownElementBuilder {
  _CodeBlockBuilder(this.base);

  final TextStyle base;

  @override
  bool isBlockElement() => true;

  @override
  Widget? visitElementAfterWithContext(
    BuildContext context,
    md.Element element,
    TextStyle? preferredStyle,
    TextStyle? parentStyle,
  ) => markdownCodeBlock(context, element, base);
}

class _RulePadding extends MarkdownPaddingBuilder {
  @override
  EdgeInsets getPadding() => const EdgeInsets.symmetric(vertical: 10);
}

class _ImageAlt extends StatelessWidget {
  const _ImageAlt(this.alt, this.c);

  final String? alt;
  final HiveColors c;

  @override
  Widget build(BuildContext context) {
    return Text.rich(
      TextSpan(
        children: [
          WidgetSpan(
            alignment: PlaceholderAlignment.middle,
            child: Icon(Icons.broken_image_outlined, size: 16, color: c.muted),
          ),
          if (alt != null && alt!.isNotEmpty) TextSpan(text: ' $alt'),
        ],
      ),
      style: TextStyle(fontSize: Fs.meta, color: c.muted),
    );
  }
}

/// 同一份文档里的图片翻回来不重新下载；只留最近几张，免得长文档的图一直占着内存。
final _images = <String, Future<Uint8List?>>{};

Future<Uint8List?> _loadImage(
  HiveAppModel model,
  String worktree,
  String path,
) {
  final key = '$worktree\n$path';
  final hit = _images.remove(key);
  if (hit != null) return _images[key] = hit;
  if (_images.length >= 24) _images.remove(_images.keys.first);
  final rpc = model.rpc;
  final future = rpc == null
      ? Future<Uint8List?>.value()
      : rpc
            .call('files.readPreview', {
              'worktree': worktree,
              'relativePath': path,
            })
            .then((res) {
              final content = res['content'];
              return res['isImage'] == true && content is String
                  ? base64Decode(content)
                  : null;
            })
            .catchError((Object _) => null);
  // 失败的不留着，下次打开再试
  future.then((v) {
    if (v == null) _images.remove(key);
  });
  return _images[key] = future;
}

class _WorkspaceImage extends StatefulWidget {
  const _WorkspaceImage({
    required this.model,
    required this.worktree,
    required this.path,
    this.alt,
  });

  final HiveAppModel model;
  final String worktree;
  final String path;
  final String? alt;

  @override
  State<_WorkspaceImage> createState() => _WorkspaceImageState();
}

/// 图只在出现时和路径变了时取一次：失败的不进缓存，在 build 里取的话每次重建（键盘、切源码预览）
/// 都会再请求一遍、闪一下转圈。
class _WorkspaceImageState extends State<_WorkspaceImage> {
  late Future<Uint8List?> _image = _load();

  Future<Uint8List?> _load() =>
      _loadImage(widget.model, widget.worktree, widget.path);

  @override
  void didUpdateWidget(_WorkspaceImage old) {
    super.didUpdateWidget(old);
    if (old.worktree != widget.worktree || old.path != widget.path) {
      _image = _load();
    }
  }

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final alt = widget.alt;
    final path = widget.path;
    return FutureBuilder<Uint8List?>(
      future: _image,
      builder: (context, snap) {
        final bytes = snap.data;
        if (bytes != null) {
          return ClipRRect(
            borderRadius: BorderRadius.circular(Rd.button),
            child: Image.memory(bytes, gaplessPlayback: true),
          );
        }
        if (snap.connectionState != ConnectionState.done) {
          return Container(
            height: 120,
            alignment: Alignment.center,
            decoration: BoxDecoration(
              color: c.code,
              borderRadius: BorderRadius.circular(Rd.button),
            ),
            child: const SizedBox.square(
              dimension: 18,
              child: CircularProgressIndicator(strokeWidth: 2),
            ),
          );
        }
        return _ImageAlt(alt ?? path, c);
      },
    );
  }
}
