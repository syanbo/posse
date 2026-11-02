import 'dart:convert';
import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter_svg/flutter_svg.dart';
import 'package:posse_quic/posse_quic.dart' as quic;

import '../l10n/l10n.dart';
import 'theme.dart';

// Mermaid 代码块画成图。排版在编进 App 的 Go 库里（posse_quic 的 renderMermaid，和桌面同一套 Pele）；
// 图形是不带文字的 SVG，交给 flutter_svg，文字用 App 的字体自己画。Go 那边量不了手机的字体，
// 也不能在排版中途同步回调 Dart，所以先在这里量好可能用到的字的宽度，连同源码一起交过去。

/// 排版的入口，测试里换成假的。
Future<String> Function(String request) mermaidRender = quic.renderMermaid;

// 和 internal/mermaid 的样式位一致
const _bold = 1;
const _italic = 2;
const _mono = 4;
const _sub = 8;
const _sup = 16;
const _smaller = 0.8;

/// 量字宽用的字号：Pele 给的字号按它等比换算。
const _base = 16.0;

/// 标签的文字样式：量宽度和画用同一个。[font] 是正文的字体（主题里的字体族和回退），
/// TextPainter 不在组件树里，不会自己继承。
TextStyle _labelStyle(TextStyle font, int style, double size, [Color? color]) {
  if (style & (_sub | _sup) != 0) size *= _smaller;
  final t = style & _mono != 0
      ? mono(size, color: color)
      : font.copyWith(
          fontSize: size,
          color: color,
          fontWeight: style & _bold != 0 ? Fw.bold : Fw.body,
        );
  return t.copyWith(
    fontStyle: style & _italic != 0 ? FontStyle.italic : FontStyle.normal,
    height: 1,
  );
}

/// 量过的字宽，按正文字体、样式表（r 正文、b 粗体、i 斜体、bi 粗斜体、m 等宽）记，App 里一直留着。
/// 表名和 posse_quic/go/mermaid.go 的 mermaidRequest.Advances 一致。
final _advances = <String, Map<String, Map<String, double>>>{};

/// ASCII 总是量上：Pele 自己会生成源码里没有的文字（坐标轴的数、饼图的百分比）。
final _ascii = String.fromCharCodes([
  for (var c = 0x20; c < 0x7f; c++) c,
  0x2026,
]);

double _measure(TextStyle font, String ch, int style) {
  final tp = TextPainter(
    text: TextSpan(text: ch, style: _labelStyle(font, style, _base)),
    textDirection: TextDirection.ltr,
    textScaler: TextScaler.noScaling,
  )..layout();
  final w = tp.width;
  tp.dispose();
  return w;
}

const _tables = [
  ('r', 0),
  ('b', _bold),
  ('i', _italic),
  ('bi', _bold | _italic),
  ('m', _mono),
];

/// 这张图要用的字宽：源码里的字和 ASCII，没量过的先量（界面线程上，量过的记着）。
/// 只交这张图用得到的，不把攒下来的全集都传过去。
Map<String, Map<String, double>> _advancesFor(TextStyle font, String source) {
  final all = _advances.putIfAbsent(_fontKey(font), () => {});
  final chars = {
    for (final r in (source + _ascii).runes)
      if (r >= 0x20) String.fromCharCode(r),
  };
  return {
    for (final (table, style) in _tables)
      table: () {
        final t = all.putIfAbsent(table, () => {});
        return {
          for (final ch in chars)
            ch: t.putIfAbsent(ch, () => _measure(font, ch, style)),
        };
      }(),
  };
}

String _fontKey(TextStyle font) =>
    '${font.fontFamily}|${font.fontFamilyFallback?.join(',')}';

class _Label {
  _Label(
    this.text,
    this.size,
    this.style,
    this.x,
    this.y,
    this.angle,
    this.color,
  );
  final String text;
  final double size, x, y, angle;
  final int style;
  final Color color;
}

class MermaidResult {
  MermaidResult._(this.svg, this.width, this.height, this._labels, this.error);
  final String svg;
  late final TextStyle _font;

  /// 排好的标签和它顶边到基线的距离：全屏页缩放、拖动时每帧都重画，只排一次。
  late final List<(_Label, TextPainter, double)> _painted = [
    for (final l in _labels)
      () {
        final tp = TextPainter(
          text: TextSpan(
            text: l.text,
            style: _labelStyle(_font, l.style, l.size, l.color),
          ),
          textDirection: TextDirection.ltr,
          textScaler: TextScaler.noScaling,
        )..layout();
        return (
          l,
          tp,
          tp.computeDistanceToActualBaseline(TextBaseline.alphabetic),
        );
      }(),
  ];
  final double width, height;
  final List<_Label> _labels;

  /// 画不出来的原因，为空表示画好了。
  final String error;

  factory MermaidResult._parse(String json) {
    final m = jsonDecode(json) as Map<String, dynamic>;
    final err = m['err'] as String? ?? '';
    Color color(String s) =>
        Color(int.parse(s.substring(7, 9) + s.substring(1, 7), radix: 16));
    return MermaidResult._(
      m['svg'] as String? ?? '',
      (m['w'] as num?)?.toDouble() ?? 0,
      (m['h'] as num?)?.toDouble() ?? 0,
      [
        for (final l in (m['labels'] as List?) ?? const [])
          _Label(
            l['t'] as String,
            (l['s'] as num).toDouble(),
            l['st'] as int,
            (l['x'] as num).toDouble(),
            (l['y'] as num).toDouble(),
            (l['a'] as num?)?.toDouble() ?? 0,
            color(l['c'] as String),
          ),
      ],
      err.isEmpty && (m['w'] == null || m['h'] == null)
          ? 'the diagram has no size'
          : err,
    );
  }
}

String _hex(Color c) =>
    '#${(c.toARGB32() & 0xFFFFFF).toRadixString(16).padLeft(6, '0')}';

/// 按源码、深浅色和字体缓存，最多 64 张，最久没用的先丢。
final _cache = <String, Future<MermaidResult>>{};

/// 测试之间清掉缓存：上一个测试的 Future 属于它的假时钟，新测试等不到它的回调；
/// 测试中途加载字体后，之前量的字宽也不对了。
@visibleForTesting
void resetMermaidCache() {
  _cache.clear();
  _advances.clear();
}

Future<MermaidResult> _diagram(String source, PosseColors c, TextStyle font) {
  final key = '${c.dark}\u0000${_fontKey(font)}\u0000$source';
  final hit = _cache.remove(key);
  if (hit != null) return _cache[key] = hit;
  // 和桌面一样：画在代码块的底上，节点是中性的次级填充，强调色只给 Pele 标重点的地方
  final surface = Color.alphaBlend(c.fill, c.code);
  final request = jsonEncode({
    'source': source,
    'style': {
      'bg': _hex(c.code),
      'fg': _hex(c.text),
      'muted': _hex(c.muted),
      'line': _hex(c.muted),
      'surface': _hex(surface),
      'surfaceAlt': _hex(Color.alphaBlend(c.fill, surface)),
      'border': _hex(c.faint),
      'accent': _hex(c.accent),
      'danger': _hex(c.danger),
    },
    'key': _fontKey(font),
    'base': _base,
    'advances': _advancesFor(font, source),
  });
  final f = mermaidRender(request)
      .then((json) => MermaidResult._parse(json).._font = font);
  if (_cache.length >= 64) _cache.remove(_cache.keys.first);
  return _cache[key] = f;
}

/// Mermaid 代码块：画好之前、画不出来、切到源码时照代码块显示（[source]）。[card] 搭外面的卡片：
/// 第一个参数是语言栏右边的按钮，第二个是正文。
class MermaidBlock extends StatefulWidget {
  const MermaidBlock({
    super.key,
    required this.code,
    required this.source,
    required this.card,
  });

  final String code;
  final Widget source;
  final Widget Function(Widget? trailing, Widget body) card;

  @override
  State<MermaidBlock> createState() => _MermaidBlockState();
}

class _MermaidBlockState extends State<MermaidBlock> {
  var _showSource = false;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final text = Theme.of(context).textTheme.bodyMedium;
    final font = TextStyle(
      fontFamily: text?.fontFamily,
      fontFamilyFallback: text?.fontFamilyFallback,
    );
    return FutureBuilder<MermaidResult>(
      future: _diagram(widget.code, c, font),
      builder: (context, snap) {
        final d = snap.data;
        // 排版本身失败（比如 Go 库加载不了）也报出来，不默默只显示源码
        final error = snap.hasError ? '${snap.error}' : d?.error ?? '';
        if (d == null && error.isEmpty) return widget.card(null, widget.source);
        if (d == null || error.isNotEmpty) {
          return widget.card(
            null,
            Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                Padding(
                  padding: const EdgeInsets.fromLTRB(12, 8, 12, 0),
                  child: Text(
                    context.l10n.mermaidCantDraw(
                      mermaidErrorText(context.l10n, error),
                    ),
                    style: TextStyle(fontSize: Fs.small, color: c.danger),
                  ),
                ),
                widget.source,
              ],
            ),
          );
        }
        final toggle = GestureDetector(
          behavior: HitTestBehavior.opaque,
          onTap: () => setState(() => _showSource = !_showSource),
          child: Padding(
            padding: const EdgeInsets.only(left: 12),
            child: Icon(
              _showSource ? Icons.visibility_rounded : Icons.code_rounded,
              size: Ic.small,
              color: c.muted,
            ),
          ),
        );
        if (_showSource) return widget.card(toggle, widget.source);
        return widget.card(
          toggle,
          Padding(
            padding: const EdgeInsets.fromLTRB(12, 10, 12, 12),
            child: GestureDetector(
              onTap: () => Navigator.of(context).push(
                MaterialPageRoute<void>(
                  builder: (_) => _MermaidZoom(diagram: d),
                ),
              ),
              child: Center(child: MermaidDiagram(d)),
            ),
          ),
        );
      },
    );
  }
}

/// 按原始大小画，比可用宽度宽就等比缩小。
class MermaidDiagram extends StatelessWidget {
  const MermaidDiagram(this.diagram, {super.key});

  final MermaidResult diagram;

  @override
  Widget build(BuildContext context) {
    final d = diagram;
    return LayoutBuilder(
      builder: (context, box) {
        final scale = box.maxWidth.isFinite
            ? math.min(1.0, box.maxWidth / d.width)
            : 1.0;
        return SizedBox(
          width: d.width * scale,
          height: d.height * scale,
          child: FittedBox(child: _natural(d)),
        );
      },
    );
  }
}

Widget _natural(MermaidResult d) => SizedBox(
  width: d.width,
  height: d.height,
  child: Stack(
    children: [
      SvgPicture.string(d.svg, width: d.width, height: d.height),
      CustomPaint(size: Size(d.width, d.height), painter: _Labels(d)),
    ],
  ),
);

class _Labels extends CustomPainter {
  _Labels(this.d);
  final MermaidResult d;

  @override
  void paint(Canvas canvas, Size size) {
    for (final (l, tp, base) in d._painted) {
      canvas.save();
      canvas.translate(l.x, l.y);
      if (l.angle != 0) canvas.rotate(l.angle * math.pi / 180);
      tp.paint(canvas, Offset(0, -base));
      canvas.restore();
    }
  }

  @override
  bool shouldRepaint(_Labels old) => old.d != d;
}

/// 点图打开的全屏页，和看 Git 里的图片一样：双指放大看细节，图形和文字都是矢量的，放大了也清楚。
class _MermaidZoom extends StatelessWidget {
  const _MermaidZoom({required this.diagram});

  final MermaidResult diagram;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text('mermaid')),
      body: ColoredBox(
        color: PosseColors.of(context).code,
        child: InteractiveViewer(
          maxScale: 8,
          child: Center(
            child: Padding(
              padding: const EdgeInsets.all(16),
              child: MermaidDiagram(diagram),
            ),
          ),
        ),
      ),
    );
  }
}

/// 手机上的 Go 库（internal/mermaid）自己的几条报错是英文源文本，按界面语言写；Pele 的语法报错原样给。
String mermaidErrorText(AppLocalizations l, String error) => switch (error) {
  'Pele did not draw an SVG' => l.mermaidNoSvg,
  'drawing the diagram timed out' => l.mermaidTimeout,
  'the diagram has no size' => l.mermaidNoSize,
  _ => error,
};
