import 'package:flutter/material.dart';
import 'package:markdown/markdown.dart' as md;

import '../ui/highlight.dart';
import '../ui/theme.dart';

class MarkdownView extends StatelessWidget {
  const MarkdownView(this.data, {super.key, this.style});

  final String data;
  final TextStyle? style;

  @override
  Widget build(BuildContext context) {
    final base = style ?? DefaultTextStyle.of(context).style;
    final nodes = _parse(data);
    if (nodes.isEmpty) return Text(data, style: base);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [for (final node in nodes) _block(context, node, base)],
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

Widget _block(BuildContext context, md.Node node, TextStyle base) {
  if (node is md.Element) {
    switch (node.tag) {
      case 'p':
        return Padding(
          padding: const EdgeInsets.only(bottom: 6),
          child: Text.rich(TextSpan(children: _inline(context, node, base))),
        );
      case 'pre':
        return markdownCodeBlock(context, node, base);
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
                        ..._inline(context, child, base),
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
              ),
            ),
          ),
        );
      case 'h1':
      case 'h2':
      case 'h3':
        final size = switch (node.tag) {
          'h1' => 17.0,
          'h2' => 15.5,
          _ => 14.0,
        };
        return Padding(
          padding: const EdgeInsets.only(top: 4, bottom: 6),
          child: Text.rich(
            TextSpan(
              style: base.copyWith(
                fontWeight: FontWeight.w600,
                fontSize: size,
                height: 1.35,
              ),
              children: _inline(context, node, base),
            ),
          ),
        );
      default:
        return Text.rich(TextSpan(children: _inline(context, node, base)));
    }
  }
  return Text(node.textContent, style: base);
}

List<InlineSpan> _inline(BuildContext context, md.Node node, TextStyle base) {
  if (node is md.Text) return [TextSpan(text: node.text, style: base)];
  if (node is! md.Element) {
    return [TextSpan(text: node.textContent, style: base)];
  }
  final style = switch (node.tag) {
    'strong' => base.copyWith(fontWeight: FontWeight.w600),
    'em' => base.copyWith(fontStyle: FontStyle.italic),
    'code' => base.copyWith(
      fontFamily: monoFamily,
      fontFamilyFallback: monoFallback,
      fontSize: (base.fontSize ?? 15) * 0.88,
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
  if (node.tag == 'code') {
    return [TextSpan(text: node.textContent, style: style)];
  }
  final children = node.children;
  if (children == null || children.isEmpty) {
    return [TextSpan(text: node.textContent, style: style)];
  }
  return [for (final child in children) ..._inline(context, child, style)];
}

/// 围栏代码块：顶上一行语言名，代码不折行、横向滚动，认得出语言时着色。文件预览也用它。
Widget markdownCodeBlock(
  BuildContext context,
  md.Element node,
  TextStyle base,
) {
  var lang = '';
  for (final child in node.children ?? const <md.Node>[]) {
    if (child is! md.Element || child.tag != 'code') continue;
    final cls = child.attributes['class'] ?? '';
    const prefix = 'language-';
    if (cls.startsWith(prefix)) lang = cls.substring(prefix.length);
  }
  final muted = _muted(context);
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
          child: Text(
            lang.isEmpty ? '代码' : lang,
            style: mono(12, color: muted),
          ),
        ),
        // 代码不折行，横向滚动，缩进才对得齐
        SingleChildScrollView(
          scrollDirection: Axis.horizontal,
          padding: const EdgeInsets.fromLTRB(12, 10, 12, 12),
          child: Highlighted(
            code: node.textContent.trimRight(),
            language: languageForFence(lang),
            builder: (context, hl) {
              final style = mono(12.5, height: 1.55, color: base.color);
              final code = node.textContent.trimRight();
              return hl == null
                  ? Text(code, softWrap: false, style: style)
                  : Text.rich(
                      codeSpan(hl, HiveColors.of(context)),
                      softWrap: false,
                      style: style,
                    );
            },
          ),
        ),
      ],
    ),
  );
}

Color _accent(BuildContext context) => HiveColors.of(context).accent;

Color _muted(BuildContext context) => HiveColors.of(context).muted;

Color _codeBg(BuildContext context) => HiveColors.of(context).code;

Color _line(BuildContext context) => HiveColors.of(context).line;
