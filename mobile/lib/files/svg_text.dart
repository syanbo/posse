import 'dart:convert';
import 'dart:typed_data';

import 'package:xml/xml.dart';

/// 改写 SVG 里 flutter_svg 画不对的文字写法，改不了的原样返回。
///
/// shields.io 这类徽章把文字放在 `<g transform="scale(.1)">` 里、字号写 110：flutter_svg 换算了
/// 位置却不缩字号，字画成 110 号、溢出 20 高的徽章被裁掉。只有文字的这种组去掉缩放，折进里面
/// 文字的坐标和字号。带 `filter` 的文字是模糊的阴影，flutter_svg 不画滤镜、会成一层清楚的深色字，去掉。
Uint8List fixSvgText(Uint8List bytes) {
  final String src;
  try {
    src = utf8.decode(bytes);
  } catch (_) {
    return bytes;
  }
  if (!src.contains('<text')) return bytes;
  final XmlDocument doc;
  try {
    doc = XmlDocument.parse(src);
  } catch (_) {
    return bytes;
  }
  var changed = false;

  void walk(XmlElement e, double scale, double? fontSize) {
    fontSize = _num(e.getAttribute('font-size')) ?? fontSize;
    final name = e.name.local;
    if (name == 'text' || name == 'tspan') {
      if (e.getAttribute('filter') != null) {
        e.remove();
        changed = true;
        return;
      }
      if (scale != 1) {
        for (final attr in ['x', 'y', 'dx', 'dy', 'textLength']) {
          final v = _num(e.getAttribute(attr));
          if (v != null) e.setAttribute(attr, _fmt(v * scale));
        }
        if (fontSize != null) {
          e.setAttribute('font-size', _fmt(fontSize * scale));
        }
        changed = true;
      }
      // 里面的 tspan 同样折算：坐标照样缩放，没写字号的按继承来的字号写上缩放后的值
      for (final k in e.childElements.toList()) {
        walk(k, scale, fontSize);
      }
      return;
    }
    if (name == 'g') {
      final s = _uniformScale(e.getAttribute('transform'));
      if (s != null && _onlyText(e)) {
        e.removeAttribute('transform');
        scale *= s;
        changed = true;
      }
    }
    for (final k in e.childElements.toList()) {
      walk(k, scale, fontSize);
    }
  }

  walk(doc.rootElement, 1, null);
  return changed ? Uint8List.fromList(utf8.encode(doc.toXmlString())) : bytes;
}

bool _onlyText(XmlElement e) => e.childElements.every(
  (k) =>
      k.name.local == 'text' ||
      k.name.local == 'title' ||
      k.name.local == 'g' &&
          k.getAttribute('transform') == null &&
          _onlyText(k),
);

double? _uniformScale(String? t) {
  if (t == null) return null;
  final m = RegExp(
    r'^\s*scale\(\s*([-\d.eE]+)\s*(?:[,\s]\s*([-\d.eE]+)\s*)?\)\s*$',
  ).firstMatch(t);
  if (m == null) return null;
  final x = double.tryParse(m[1]!);
  final y = m[2] == null ? x : double.tryParse(m[2]!);
  return x != null && x == y && x > 0 ? x : null;
}

double? _num(String? s) {
  if (s == null) return null;
  final t = s.trim();
  return double.tryParse(t.endsWith('px') ? t.substring(0, t.length - 2) : t);
}

String _fmt(double v) {
  final r = (v * 1000).roundToDouble() / 1000;
  return r == r.truncateToDouble() ? '${r.toInt()}' : '$r';
}
