import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:math' as math;
import 'dart:typed_data';

import 'package:flutter/gestures.dart';
import 'package:flutter/material.dart';
import 'package:flutter_markdown_plus/flutter_markdown_plus.dart';
import 'package:flutter_svg/flutter_svg.dart';
import 'package:markdown/markdown.dart' as md;
import 'package:url_launcher/url_launcher.dart';

import '../chat/markdown_view.dart';
import '../app_state.dart';
import '../protocol/rpc.dart' show rpcBytes;
import '../terminal/link_target.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import 'markdown_html.dart';
import 'svg_text.dart';

/// 文件页里渲染一个 Markdown 文件：表格、任务列表、嵌套列表、图片都画出来，正文里的 HTML 照 GitHub 解释
/// （[HtmlDoc]）。相对链接和图片按这个文件所在的目录算，工作区里的文件在文件页打开、图片向电脑要。
/// 自己滚动，只排看得见的块：长文档整篇排进一列，打开那一帧要几十毫秒。
class MarkdownDoc extends StatefulWidget {
  const MarkdownDoc({
    super.key,
    required this.data,
    required this.model,
    required this.worktree,
    required this.path,
    required this.openFile,
  });

  final String data;
  final PosseAppModel model;
  final String worktree;

  /// 这个 md 文件相对工作区根目录的路径。
  final String path;
  final void Function(String path) openFile;

  @override
  State<MarkdownDoc> createState() => _MarkdownDocState();
}

/// 自己解析、换掉 HTML 后交给 flutter_markdown_plus 的 [MarkdownBuilder]：它的 Markdown 控件只收原文。
/// 对齐、HTML 表格和折叠里的块由同一个 builder 再建一遍（[_build]）。
class _MarkdownDocState extends State<MarkdownDoc>
    implements MarkdownBuilderDelegate {
  final _recognizers = <GestureRecognizer>[];
  HtmlDoc? _doc;
  String? _parsed;
  List<Widget> _children = const [];

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    _rebuild();
  }

  @override
  void didUpdateWidget(MarkdownDoc old) {
    super.didUpdateWidget(old);
    if (old.data != widget.data ||
        old.worktree != widget.worktree ||
        old.path != widget.path) {
      _rebuild();
    }
  }

  @override
  void dispose() {
    _disposeRecognizers();
    super.dispose();
  }

  void _disposeRecognizers() {
    for (final r in _recognizers) {
      r.dispose();
    }
    _recognizers.clear();
  }

  void _rebuild() {
    _disposeRecognizers();
    if (_parsed != widget.data) {
      final nodes = md.Document(
        extensionSet: markdownExtensions,
        encodeHtml: false,
      ).parseLines(const LineSplitter().convert(widget.data));
      _doc = HtmlDoc(nodes);
      _parsed = widget.data;
    }
    _children = _build(_doc!.nodes);
  }

  List<Widget> _build(List<md.Node> nodes, {WrapAlignment? align}) {
    final c = PosseColors.of(context);
    final base = TextStyle(fontSize: Fs.body, height: Lh.read, color: c.text);
    var sheet = MarkdownStyleSheet.fromTheme(Theme.of(context))
        .merge(markdownDocStyle(c, base))
        .copyWith(textScaler: MediaQuery.textScalerOf(context));
    if (align != null) {
      sheet = sheet.copyWith(
        textAlign: align,
        h1Align: align,
        h2Align: align,
        h3Align: align,
        h4Align: align,
        h5Align: align,
        h6Align: align,
      );
    }
    return MarkdownBuilder(
      delegate: this,
      selectable: false,
      styleSheet: sheet,
      imageDirectory: null,
      imageBuilder: null,
      checkboxBuilder: (checked) => Padding(
        padding: const EdgeInsets.only(right: 6),
        child: Icon(
          checked
              ? Icons.check_box_rounded
              : Icons.check_box_outline_blank_rounded,
          size: Ic.medium,
          color: checked ? c.accent : c.muted,
        ),
      ),
      bulletBuilder: (p) => _bullet(p, base, c),
      builders: {
        'pre': _CodeBlockBuilder(base),
        'img': _Builder(inline: true, (e) => _image(e, c)),
        'x-align': _Builder((e) {
          final b = _block(e);
          final a = switch (b.align) {
            'center' => WrapAlignment.center,
            'right' => WrapAlignment.end,
            _ => WrapAlignment.start,
          };
          return _column(_build(b.blocks, align: a), switch (a) {
            WrapAlignment.center => CrossAxisAlignment.center,
            WrapAlignment.end => CrossAxisAlignment.end,
            _ => CrossAxisAlignment.start,
          });
        }),
        'x-grid': _Builder((e) => _grid(_block(e), c)),
        'x-details': _Builder((e) {
          final b = _block(e);
          return _Details(
            open: b.open,
            summary: _column(_build([md.Element('p', b.summary)])),
            body: _column(_build(b.blocks)),
          );
        }),
      },
      paddingBuilders: {'hr': _RulePadding()},
      listItemCrossAxisAlignment: MarkdownListItemCrossAxisAlignment.baseline,
    ).build(nodes);
  }

  HtmlBlock _block(md.Element e) =>
      _doc!.blocks[int.parse(e.attributes['ref']!)];

  Widget _column(
    List<Widget> children, [
    CrossAxisAlignment cross = CrossAxisAlignment.stretch,
  ]) => Column(
    mainAxisSize: MainAxisSize.min,
    crossAxisAlignment: cross,
    children: children,
  );

  /// HTML 表格：列宽照 width 写的百分比分，没写的平分剩下的；外框、格线和表头的底色同 Markdown 表格。
  Widget _grid(HtmlBlock b, PosseColors c) {
    final cols = b.grid.fold(0, (n, r) => math.max(n, r.length));
    if (cols == 0) return const SizedBox.shrink();
    final pct = List<double?>.filled(cols, null);
    for (final row in b.grid) {
      for (final (i, cell) in row.indexed) {
        final w = cell.width;
        if (pct[i] == null && w != null && w.pct) pct[i] = w.value;
      }
    }
    final used = pct.fold(0.0, (s, v) => s + (v ?? 0));
    final unset = pct.where((v) => v == null).length;
    final rest = unset == 0 ? 0.0 : math.max(100 - used, 10.0 * unset) / unset;
    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: Table(
        border: TableBorder.all(color: c.line),
        columnWidths: {
          for (var i = 0; i < cols; i++) i: FlexColumnWidth(pct[i] ?? rest),
        },
        children: [
          for (final row in b.grid)
            TableRow(
              children: [
                for (var i = 0; i < cols; i++)
                  if (i < row.length)
                    TableCell(
                      verticalAlignment: switch (row[i].valign) {
                        'top' => TableCellVerticalAlignment.top,
                        'bottom' => TableCellVerticalAlignment.bottom,
                        _ => TableCellVerticalAlignment.middle,
                      },
                      child: Container(
                        color: row[i].header ? c.code : null,
                        padding: const EdgeInsets.symmetric(
                          horizontal: 10,
                          vertical: 6,
                        ),
                        child: _column(
                          _build(
                            row[i].blocks,
                            align: switch (row[i].align) {
                              'center' => WrapAlignment.center,
                              'right' => WrapAlignment.end,
                              _ => row[i].header ? WrapAlignment.center : null,
                            },
                          ),
                          switch (row[i].align ??
                              (row[i].header ? 'center' : null)) {
                            'center' => CrossAxisAlignment.center,
                            'right' => CrossAxisAlignment.end,
                            _ => CrossAxisAlignment.start,
                          },
                        ),
                      ),
                    )
                  else
                    const SizedBox.shrink(),
              ],
            ),
        ],
      ),
    );
  }

  @override
  GestureRecognizer createLink(String text, String? href, String title) {
    final r = TapGestureRecognizer()..onTap = () => _tapLink(href);
    _recognizers.add(r);
    return r;
  }

  @override
  TextSpan formatText(MarkdownStyleSheet styleSheet, String code) => TextSpan(
    style: styleSheet.code,
    text: code.replaceAll(RegExp(r'\n$'), ''),
  );

  @override
  Widget build(BuildContext context) {
    // 表格的横向滚动条在 iOS 上是 CupertinoScrollbar，会按底部安全区抬高，压在表格最后一行上；
    // 去掉底部安全区，滚动条落回表格下面的空隙。正文自己有底边距，不靠安全区
    return MediaQuery.removePadding(
      context: context,
      removeBottom: true,
      child: ListView(
        padding: const EdgeInsets.fromLTRB(16, 8, 16, 32),
        children: _children,
      ),
    );
  }

  Widget _bullet(MarkdownBulletParameters p, TextStyle base, PosseColors c) {
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

  Widget _image(md.Element e, PosseColors c) {
    final a = e.attributes;
    final alt = a['alt'];
    final src = mdImageSource(
      a['src'] ?? '',
      worktree: widget.worktree,
      docPath: widget.path,
    );
    Widget child = src == null
        ? _ImageAlt(alt, c)
        : _MdImage(
            model: widget.model,
            source: src,
            alt: alt,
            width: HtmlLen.parse(a['width']),
            height: HtmlLen.parse(a['height']),
          );
    final href = a['href'];
    if (href != null) {
      child = GestureDetector(onTap: () => _tapLink(href), child: child);
    }
    return child;
  }

  void _tapLink(String? href) {
    if (href == null || href.isEmpty || href.startsWith('#')) return;
    final uri = Uri.tryParse(href);
    if (uri == null) return;
    final root = widget.model.worktrees
        .where((w) => w.id == widget.worktree)
        .firstOrNull
        ?.path;
    final LinkTarget? target;
    if (!uri.hasScheme && !uri.path.startsWith('/')) {
      final rel = _relative(uri.path, widget.path);
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
        widget.openFile(path);
      case NotInWorkspace():
        flash('这个文件不在当前工作区里，手机上打不开');
      case null:
        break;
    }
  }
}

/// 相对 md 文件 [doc] 的路径换成相对工作区根目录的；指到工作区外面时为 null。
String? _relative(String raw, String doc) {
  raw = Uri.decodeComponent(raw);
  if (raw.isEmpty || raw.startsWith('/')) return null;
  final slash = doc.lastIndexOf('/');
  final dir = slash < 0 ? '' : doc.substring(0, slash + 1);
  final target = resolveLink(filePath: '$dir$raw');
  return target is OpenFile ? target.path : null;
}

/// 图片从哪儿取：[url] 是网址或 `data:` 地址，否则是工作区里的 [path]。
class MdImageSource {
  const MdImageSource.url(String this.url) : worktree = '', path = null;
  const MdImageSource.file(this.worktree, String this.path) : url = null;

  final String? url;
  final String worktree;
  final String? path;

  String get key => url ?? '$worktree\n$path';
}

/// 照 GitHub 找图片：相对路径按 md 文件所在目录，`/` 开头的按工作区根目录，去掉 `?raw=true` 这类后缀；
/// 网址和 `data:` 地址原样。别的协议和指到工作区外面的为 null。
MdImageSource? mdImageSource(
  String src, {
  required String worktree,
  required String docPath,
}) {
  src = src.trim();
  if (src.startsWith('data:')) return MdImageSource.url(src);
  final uri = Uri.tryParse(src);
  if (uri == null) return null;
  if (uri.scheme == 'http' || uri.scheme == 'https') {
    return MdImageSource.url(src);
  }
  if (uri.hasScheme && uri.scheme != 'file') return null;
  final path = uri.path;
  if (path.startsWith('/')) {
    final target = resolveLink(
      filePath: Uri.decodeComponent(path.substring(1)),
    );
    return target is OpenFile
        ? MdImageSource.file(worktree, target.path)
        : null;
  }
  final rel = _relative(path, docPath);
  return rel == null ? null : MdImageSource.file(worktree, rel);
}

class _Builder extends MarkdownElementBuilder {
  _Builder(this.build, {this.inline = false});

  final Widget Function(md.Element e) build;
  final bool inline;

  @override
  bool isBlockElement() => !inline;

  @override
  Widget? visitElementAfterWithContext(
    BuildContext context,
    md.Element element,
    TextStyle? preferredStyle,
    TextStyle? parentStyle,
  ) => build(element);
}

/// `<details>`：`<summary>` 一行可点，前面的箭头展开时转向下；内容缩进一格。
class _Details extends StatefulWidget {
  const _Details({
    required this.open,
    required this.summary,
    required this.body,
  });

  final bool open;
  final Widget summary;
  final Widget body;

  @override
  State<_Details> createState() => _DetailsState();
}

class _DetailsState extends State<_Details> {
  late bool _open = widget.open;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          GestureDetector(
            behavior: HitTestBehavior.opaque,
            onTap: () => setState(() => _open = !_open),
            child: Row(
              children: [
                AnimatedRotation(
                  turns: _open ? 0.25 : 0,
                  duration: const Duration(milliseconds: 150),
                  child: Icon(
                    Icons.chevron_right_rounded,
                    size: Ic.medium,
                    color: c.muted,
                  ),
                ),
                const SizedBox(width: 4),
                Expanded(child: IgnorePointer(child: widget.summary)),
              ],
            ),
          ),
          if (_open)
            Padding(
              padding: const EdgeInsets.only(left: Ic.medium + 4, top: 4),
              child: widget.body,
            ),
        ],
      ),
    );
  }
}

/// 文件预览的排版：标题加粗，块之间留足空隙，表格带边框、太宽时左右滑。
MarkdownStyleSheet markdownDocStyle(PosseColors c, TextStyle base) {
  TextStyle heading(double size) =>
      base.copyWith(fontSize: size, fontWeight: Fw.bold, height: Lh.title);
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
      fontSize: Fs.small,
      color: c.accent,
      backgroundColor: c.code,
    ),
    h1: heading(Fs.heading),
    h1Padding: const EdgeInsets.only(top: 12),
    h2: heading(Fs.title),
    h2Padding: const EdgeInsets.only(top: 12),
    h3: heading(Fs.body),
    h3Padding: const EdgeInsets.only(top: 8),
    h4: heading(Fs.body),
    h4Padding: const EdgeInsets.only(top: 6),
    h5: heading(Fs.body).copyWith(color: c.muted),
    h6: heading(Fs.body).copyWith(color: c.muted),
    strong: const TextStyle(fontWeight: Fw.bold),
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
    tableHead: base.copyWith(fontSize: Fs.body, fontWeight: Fw.bold),
    tableBody: base.copyWith(fontSize: Fs.body, height: Lh.code),
    tableHeadAlign: TextAlign.left,
    tablePadding: const EdgeInsets.only(bottom: 10),
    tableBorder: TableBorder.all(color: c.line),
    // 按内容宽排、超出屏幕左右滑（只有固定宽和内容宽两种才会套横向滚动）；
    // 一列最宽 220，长句子在格子里折行，不会一行拉得比屏幕宽好几倍
    tableColumnWidth: const _CappedColumnWidth(220),
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

/// 按内容宽、但不超过 [cap] 的列宽；放不下的长词（路径、代码）仍按它自己的宽度。
/// 继承 IntrinsicColumnWidth：flutter_markdown_plus 只给它和固定宽的表格套横向滚动。
class _CappedColumnWidth extends IntrinsicColumnWidth {
  const _CappedColumnWidth(this.cap);

  final double cap;

  @override
  double maxIntrinsicWidth(Iterable<RenderBox> cells, double containerWidth) {
    final natural = super.maxIntrinsicWidth(cells, containerWidth);
    final word = super.minIntrinsicWidth(cells, containerWidth);
    return math.min(natural, math.max(cap, word));
  }
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

/// 图片的说明文字：载入中前面是转圈，载不了是裂开的图。
class _ImageAlt extends StatelessWidget {
  const _ImageAlt(this.alt, this.c, {this.loading = false});

  final String? alt;
  final PosseColors c;
  final bool loading;

  @override
  Widget build(BuildContext context) {
    return Text.rich(
      TextSpan(
        children: [
          WidgetSpan(
            alignment: PlaceholderAlignment.middle,
            child: loading
                ? const HexSpinner(size: Ic.small)
                : Icon(
                    Icons.broken_image_rounded,
                    size: Ic.small,
                    color: c.muted,
                  ),
          ),
          TextSpan(text: ' ${alt == null || alt!.isEmpty ? '图片' : alt}'),
        ],
      ),
      style: TextStyle(fontSize: Fs.small, color: c.muted),
    );
  }
}

/// 同一份文档里的图片翻回来不重新下载；只留最近几张，免得长文档的图一直占着内存。
final _images = <String, Future<Uint8List?>>{};

/// 网上的图照桌面：20 秒超时，最大 20 MB。
const _maxDownload = 20 << 20;

Future<Uint8List?> _loadImage(PosseAppModel model, MdImageSource src) {
  final url = src.url;
  if (url != null && url.startsWith('data:')) {
    try {
      return Future.value(UriData.parse(url).contentAsBytes());
    } catch (_) {
      return Future.value();
    }
  }
  final key = src.key;
  final hit = _images.remove(key);
  if (hit != null) return _images[key] = hit;
  if (_images.length >= 24) _images.remove(_images.keys.first);
  final Future<Uint8List?> future;
  if (url != null) {
    future = _download(url).catchError((Object _) => null);
  } else {
    final rpc = model.rpc;
    future = rpc == null
        ? Future<Uint8List?>.value()
        : rpc
              .call('files.readPreview', {
                'worktree': src.worktree,
                'relativePath': src.path,
              })
              .then((res) {
                final content = res['content'];
                return res['isImage'] == true &&
                        (content is String || content is Uint8List)
                    ? rpcBytes(content)
                    : null;
              })
              .catchError((Object _) => null);
  }
  // 失败的不留着，下次打开再试
  future.then((v) {
    if (v == null) _images.remove(key);
  });
  return _images[key] = future;
}

Future<Uint8List?> _download(String url) async {
  final client = HttpClient()..connectionTimeout = const Duration(seconds: 20);
  try {
    return await () async {
      final req = await client.getUrl(Uri.parse(url));
      final res = await req.close();
      if (res.statusCode != HttpStatus.ok) return null;
      if (res.contentLength > _maxDownload) return null;
      final out = BytesBuilder(copy: false);
      await for (final chunk in res) {
        out.add(chunk);
        if (out.length > _maxDownload) return null;
      }
      return out.takeBytes();
    }().timeout(const Duration(seconds: 20));
  } finally {
    client.close(force: true);
  }
}

/// SVG 按开头的字认（徽章的网址多半不带扩展名）。
bool _isSvg(Uint8List bytes) {
  final head = utf8
      .decode(
        bytes.sublist(0, math.min(bytes.length, 512)),
        allowMalformed: true,
      )
      .trimLeft()
      .toLowerCase();
  return head.startsWith('<svg') ||
      head.startsWith('<?xml') && head.contains('<svg') ||
      head.startsWith('<!--') && head.contains('<svg');
}

/// Markdown 里的一张图：按原图大小（写了 width、height 照写的），最宽不超过版心；
/// 载入前和载不了时显示说明文字。
class _MdImage extends StatefulWidget {
  const _MdImage({
    required this.model,
    required this.source,
    this.alt,
    this.width,
    this.height,
  });

  final PosseAppModel model;
  final MdImageSource source;
  final String? alt;
  final HtmlLen? width;
  final HtmlLen? height;

  @override
  State<_MdImage> createState() => _MdImageState();
}

/// 图只在出现时和地址变了时取一次：失败的不进缓存，在 build 里取的话每次重建（键盘、切源码预览）
/// 都会再请求一遍、闪一下。
class _MdImageState extends State<_MdImage> {
  late Future<Uint8List?> _image = _load();

  Future<Uint8List?> _load() => _loadImage(
    widget.model,
    widget.source,
  ).then((b) => b != null && _isSvg(b) ? fixSvgText(b) : b);

  @override
  void didUpdateWidget(_MdImage old) {
    super.didUpdateWidget(old);
    if (old.source.key != widget.source.key) _image = _load();
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return FutureBuilder<Uint8List?>(
      future: _image,
      builder: (context, snap) {
        final bytes = snap.data;
        if (bytes == null) {
          return _ImageAlt(
            widget.alt,
            c,
            loading: snap.connectionState != ConnectionState.done,
          );
        }
        final w = widget.width;
        final h = widget.height;
        final width = w != null && !w.pct ? w.value : null;
        final height = h != null && !h.pct ? h.value : null;
        Widget img = _isSvg(bytes)
            ? SvgPicture.memory(
                bytes,
                width: width,
                height: height,
                errorBuilder: (_, _, _) => _ImageAlt(widget.alt, c),
              )
            : Image(
                // 大图按 1600 像素解码，不放大小图
                image: ResizeImage(
                  MemoryImage(bytes),
                  width: 1600,
                  height: 1600,
                  policy: ResizeImagePolicy.fit,
                ),
                width: width,
                height: height,
                fit: BoxFit.contain,
                gaplessPlayback: true,
                errorBuilder: (_, _, _) => _ImageAlt(widget.alt, c),
              );
        if (w != null && w.pct) {
          img = FractionallySizedBox(
            widthFactor: math.min(w.value, 100) / 100,
            child: img,
          );
        }
        return img;
      },
    );
  }
}
