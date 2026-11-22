import 'dart:async';

import 'package:flutter/gestures.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:markdown/markdown.dart' as md;

import '../l10n/l10n.dart';
import '../ui/highlight.dart';
import '../ui/mermaid.dart';
import '../ui/theme.dart';

class MarkdownView extends StatefulWidget {
  const MarkdownView(
    this.data, {
    super.key,
    this.style,
    this.copyActions = false,
  });

  final String data;
  final TextStyle? style;
  final bool copyActions;

  @override
  State<MarkdownView> createState() => _MarkdownViewState();
}

class _MarkdownViewState extends State<MarkdownView> {
  late var _nodes = _parse(widget.data);
  final _links = <md.Element, LongPressGestureRecognizer>{};

  @override
  void didUpdateWidget(MarkdownView oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.data != widget.data) {
      _disposeLinks();
      _nodes = _parse(widget.data);
    }
  }

  @override
  void dispose() {
    _disposeLinks();
    super.dispose();
  }

  void _disposeLinks() {
    for (final link in _links.values) {
      link.dispose();
    }
    _links.clear();
  }

  LongPressGestureRecognizer? _linkGesture(md.Element node) {
    final address = node.attributes['href'];
    if (address == null || address.isEmpty) return null;
    return _links.putIfAbsent(
      node,
      () =>
          LongPressGestureRecognizer()
            ..onLongPress = () =>
                unawaited(_copyMarkdownText(context, address)),
    );
  }

  @override
  Widget build(BuildContext context) {
    final base = widget.style ?? DefaultTextStyle.of(context).style;
    if (_nodes.isEmpty) return Text(widget.data, style: base);
    final links = widget.copyActions ? _linkGesture : null;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [for (final node in _nodes) _block(context, node, base, links)],
    );
  }
}

/// GitHub 风格，但网址自动识别换成 [_Autolink]。对话页和文件预览共用。
final markdownExtensions = md.ExtensionSet(
  md.ExtensionSet.gitHubFlavored.blockSyntaxes,
  [
    for (final s in md.ExtensionSet.gitHubFlavored.inlineSyntaxes)
      if (s is! md.AutolinkExtensionSyntax) s,
    _Autolink(),
  ],
);

/// markdown 7.3.1 的网址自动识别允许网址里带 `]`，`[打开 https://a.com](https://a.com)` 会把
/// `https://a.com](https://a.com)` 整段吞成一个网址，外面的链接就坏了。只把 `]` 排除在网址外，其余同原版。
class _Autolink extends md.AutolinkExtensionSyntax {
  static final _pattern = RegExp(
    r'((?:(?:https?|ftp):\/\/|www\.)(?:[-_a-z0-9]+\.)*(?:[-a-z0-9]+\.[-a-z0-9]+)'
    r'[^\s<\]]*[^\s<?!.,:*_~\]])'
    r'|([-_.+a-z0-9]+@(?:[-_a-z0-9]+\.)+[-_a-z0-9]*[a-z0-9])',
    multiLine: true,
    caseSensitive: false,
  );

  @override
  RegExp get pattern => _pattern;
}

/// 对话页每秒都会重建（标签状态、状态行），同一段回复不必每次重新解析。
final _parsed = <String, List<md.Node>>{};

List<md.Node> _parse(String data) {
  final hit = _parsed.remove(data);
  if (hit != null) return _parsed[data] = hit;
  // 默认会把 < " & 编码成 HTML 实体；这里直接画文字，不经过 HTML
  final nodes = md.Document(
    extensionSet: markdownExtensions,
    encodeHtml: false,
  ).parse(data);
  if (_parsed.length >= 200) _parsed.remove(_parsed.keys.first);
  return _parsed[data] = nodes;
}

typedef _LinkGesture = LongPressGestureRecognizer? Function(md.Element);

Widget _block(
  BuildContext context,
  md.Node node,
  TextStyle base,
  _LinkGesture? links,
) {
  if (node is md.Element) {
    switch (node.tag) {
      case 'p':
        return Padding(
          padding: const EdgeInsets.only(bottom: 6),
          child: Text.rich(
            TextSpan(children: _inline(context, node, base, links)),
          ),
        );
      case 'pre':
        return markdownCodeBlock(
          context,
          node,
          base,
          copySource: links != null,
        );
      case 'ul':
      case 'ol':
        final ordered = node.tag == 'ol';
        var index = 1;
        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            for (final child in node.children ?? const <md.Node>[])
              if (child is md.Element && child.tag == 'li')
                Padding(
                  padding: const EdgeInsets.only(bottom: 2),
                  child: Text.rich(
                    TextSpan(
                      children: [
                        TextSpan(
                          text: ordered ? '${index++}. ' : '• ',
                          style: base.copyWith(color: _muted(context)),
                        ),
                        ..._inline(context, child, base, links),
                      ],
                    ),
                  ),
                ),
          ],
        );
      case 'blockquote':
        return Container(
          margin: const EdgeInsets.only(bottom: 6),
          padding: const EdgeInsets.only(left: 10),
          decoration: BoxDecoration(
            border: Border(left: BorderSide(color: _line(context), width: 3)),
          ),
          child: Text.rich(
            TextSpan(
              style: base.copyWith(color: _muted(context)),
              children: _inline(
                context,
                node,
                base.copyWith(color: _muted(context)),
                links,
              ),
            ),
          ),
        );
      case 'h1':
      case 'h2':
      case 'h3':
        final size = switch (node.tag) {
          'h1' => Fs.heading,
          'h2' => Fs.title,
          _ => Fs.body,
        };
        // 行内的每段都带自己的样式，标题样式要当 base 传进去，只设在外层会被盖掉
        final heading = base.copyWith(
          fontWeight: Fw.bold,
          fontSize: size,
          height: Lh.title,
        );
        return Padding(
          padding: const EdgeInsets.only(top: 4, bottom: 6),
          child: Text.rich(
            TextSpan(
              style: heading,
              children: _inline(context, node, heading, links),
            ),
          ),
        );
      default:
        return Text.rich(
          TextSpan(children: _inline(context, node, base, links)),
        );
    }
  }
  return Text(node.textContent, style: base);
}

List<InlineSpan> _inline(
  BuildContext context,
  md.Node node,
  TextStyle base,
  _LinkGesture? links, [
  GestureRecognizer? gesture,
]) {
  if (node is md.Text) {
    return [TextSpan(text: node.text, style: base, recognizer: gesture)];
  }
  if (node is! md.Element) {
    return [TextSpan(text: node.textContent, style: base, recognizer: gesture)];
  }
  if (node.tag == 'pre' && links != null) {
    return [
      WidgetSpan(
        child: markdownCodeBlock(context, node, base, copySource: true),
      ),
    ];
  }
  final style = switch (node.tag) {
    'strong' => base.copyWith(fontWeight: Fw.bold),
    'em' => base.copyWith(fontStyle: FontStyle.italic),
    'code' => base.copyWith(
      fontFamily: monoFamily,
      fontFamilyFallback: monoFallback,
      fontSize: Fs.small,
      color: _accent(context),
      backgroundColor: _codeBg(context),
    ),
    'a' => base.copyWith(
      color: _accent(context),
      decoration: TextDecoration.underline,
      decorationColor: _accent(context),
    ),
    _ => base,
  };
  if (node.tag == 'a') gesture = links?.call(node);
  final children = node.children;
  if (node.tag == 'code' || children == null || children.isEmpty) {
    return [
      TextSpan(text: node.textContent, style: style, recognizer: gesture),
    ];
  }
  return [
    for (final child in children)
      ..._inline(context, child, style, links, gesture),
  ];
}

Future<void> _copyMarkdownText(BuildContext context, String text) async {
  await Clipboard.setData(ClipboardData(text: text));
  if (!context.mounted) return;
  ScaffoldMessenger.maybeOf(context)
    ?..hideCurrentSnackBar()
    ..showSnackBar(SnackBar(content: Text(context.l10n.commonCopied)));
}

/// 围栏代码块：顶上一行语言名，代码不折行、横向滚动，认得出语言时着色。文件预览也用它。
/// 语言是 mermaid 的画成图（[MermaidBlock]），语言栏右边可以切回源码。
Widget markdownCodeBlock(
  BuildContext context,
  md.Element node,
  TextStyle base, {
  bool copySource = false,
}) {
  var lang = '';
  for (final child in node.children ?? const <md.Node>[]) {
    if (child is! md.Element || child.tag != 'code') continue;
    final cls = child.attributes['class'] ?? '';
    const prefix = 'language-';
    if (cls.startsWith(prefix)) lang = cls.substring(prefix.length);
  }
  final raw = node.textContent;
  final code = raw.trimRight();
  final source = _codeBody(code, lang, base);
  final copy = copySource && raw.isNotEmpty
      ? IconButton(
          tooltip: context.l10n.chatCopyCode,
          icon: const Icon(Icons.content_copy_rounded, size: Ic.small),
          visualDensity: VisualDensity.compact,
          onPressed: () => unawaited(_copyMarkdownText(context, raw)),
        )
      : null;
  if (lang.toLowerCase() == 'mermaid') {
    return MermaidBlock(
      code: code,
      source: source,
      card: (trailing, body) =>
          _codeCard(context, lang, [?trailing, ?copy], body),
    );
  }
  return _codeCard(context, lang, [?copy], source);
}

Widget _codeCard(
  BuildContext context,
  String lang,
  List<Widget> actions,
  Widget body,
) {
  return Container(
    width: double.infinity,
    margin: const EdgeInsets.only(top: 2, bottom: 10),
    decoration: BoxDecoration(
      color: _codeBg(context),
      borderRadius: BorderRadius.circular(Rd.code),
    ),
    clipBehavior: Clip.antiAlias,
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Container(
          padding: const EdgeInsets.fromLTRB(12, 6, 12, 6),
          decoration: BoxDecoration(
            border: Border(bottom: BorderSide(color: _line(context))),
          ),
          child: Row(
            children: [
              Expanded(
                child: Text(
                  lang.isEmpty ? context.l10n.chatCode : lang,
                  style: mono(Fs.small, color: _muted(context)),
                ),
              ),
              ...actions,
            ],
          ),
        ),
        body,
      ],
    ),
  );
}

// 代码不折行，横向滚动，缩进才对得齐
Widget _codeBody(String code, String lang, TextStyle base) =>
    SingleChildScrollView(
      scrollDirection: Axis.horizontal,
      padding: const EdgeInsets.fromLTRB(12, 10, 12, 12),
      child: Highlighted(
        code: code,
        language: languageForFence(lang),
        builder: (context, hl) {
          final style = mono(Fs.small, height: Lh.read, color: base.color);
          return hl == null
              ? Text(code, softWrap: false, style: style)
              : Text.rich(
                  codeSpan(hl, PosseColors.of(context)),
                  softWrap: false,
                  style: style,
                );
        },
      ),
    );

Color _accent(BuildContext context) => PosseColors.of(context).accent;

Color _muted(BuildContext context) => PosseColors.of(context).muted;

Color _codeBg(BuildContext context) => PosseColors.of(context).code;

Color _line(BuildContext context) => PosseColors.of(context).line;
