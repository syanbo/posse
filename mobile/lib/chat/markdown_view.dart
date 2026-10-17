import 'package:flutter/material.dart';
import 'package:markdown/markdown.dart' as md;

class MarkdownView extends StatelessWidget {
  const MarkdownView(this.data, {super.key, this.style});

  final String data;
  final TextStyle? style;

  @override
  Widget build(BuildContext context) {
    final base = style ?? DefaultTextStyle.of(context).style;
    final nodes = md.Document(extensionSet: md.ExtensionSet.gitHubFlavored)
        .parse(data);
    if (nodes.isEmpty) return Text(data, style: base);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [for (final node in nodes) _block(context, node, base)],
    );
  }
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
        return _codeBlock(context, node, base);
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
      fontFamily: 'monospace',
      fontSize: 12,
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

Widget _codeBlock(BuildContext context, md.Element node, TextStyle base) {
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
    margin: const EdgeInsets.only(bottom: 8),
    decoration: BoxDecoration(
      color: _codeBg(context),
      borderRadius: BorderRadius.circular(8),
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
            style: base.copyWith(
              fontFamily: 'monospace',
              fontSize: 12,
              color: muted,
            ),
          ),
        ),
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 8, 12, 10),
          child: Text(
            node.textContent.trimRight(),
            style: base.copyWith(
              fontFamily: 'monospace',
              fontSize: 12,
              height: 1.5,
            ),
          ),
        ),
      ],
    ),
  );
}

Color _accent(BuildContext context) =>
    Theme.of(context).brightness == Brightness.dark
    ? const Color(0xFFD97757)
    : const Color(0xFFC15F3C);

Color _muted(BuildContext context) =>
    Theme.of(context).brightness == Brightness.dark
    ? const Color(0xFF9C9893)
    : const Color(0xFF6F6B66);

Color _codeBg(BuildContext context) =>
    Theme.of(context).brightness == Brightness.dark
    ? const Color(0xFF141413)
    : const Color(0xFFF5F4F1);

Color _line(BuildContext context) =>
    Theme.of(context).brightness == Brightness.dark
    ? const Color(0x14FFFFFF)
    : const Color(0x14000000);
