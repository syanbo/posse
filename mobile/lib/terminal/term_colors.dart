import 'dart:ui' show Color;

/// 程序查到的终端配色（电脑窗口的终端在用的那套）：前景、底色和 16 色。
/// 常驻进程在订阅时（`subscribed`）给一份，电脑换主题时推 `colors`。
/// 程序按它配色，手机照它画终端，深浅才和程序画的一致；旧版常驻进程不给，照手机主题画。
class TermColors {
  const TermColors({
    required this.foreground,
    required this.background,
    required this.palette,
  });

  final Color foreground;
  final Color background;

  /// 16 色：黑红绿黄蓝品青白，再是各自的亮色。
  final List<Color> palette;

  /// 解析 `{"foreground": "#rrggbb", "background": …, "palette": [16 个]}`，格式不对给 null。
  static TermColors? parse(Object? raw) {
    if (raw is! Map) return null;
    final fg = _hex(raw['foreground']);
    final bg = _hex(raw['background']);
    final list = raw['palette'];
    if (fg == null || bg == null || list is! List || list.length != 16) {
      return null;
    }
    final palette = <Color>[];
    for (final v in list) {
      final c = _hex(v);
      if (c == null) return null;
      palette.add(c);
    }
    return TermColors(
      foreground: fg,
      background: bg,
      palette: List.unmodifiable(palette),
    );
  }

  static Color? _hex(Object? v) {
    if (v is! String || v.length != 7 || !v.startsWith('#')) return null;
    final n = int.tryParse(v.substring(1), radix: 16);
    if (n == null) return null;
    return Color(n | 0xFF000000);
  }

  @override
  bool operator ==(Object other) =>
      other is TermColors &&
      other.foreground == foreground &&
      other.background == background &&
      _same(other.palette, palette);

  @override
  int get hashCode =>
      Object.hash(foreground, background, Object.hashAll(palette));

  static bool _same(List<Color> a, List<Color> b) {
    if (a.length != b.length) return false;
    for (var i = 0; i < a.length; i++) {
      if (a[i] != b[i]) return false;
    }
    return true;
  }
}
