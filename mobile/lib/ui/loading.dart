import 'dart:math' as math;

import 'package:flutter/material.dart';

import 'theme.dart';

/// 整页加载：七格蜂巢，外圈六格顺时针逐格亮起，后面拖一段渐暗。
class HiveLoader extends StatefulWidget {
  const HiveLoader({super.key, this.size = 36, this.color});

  final double size;
  final Color? color;

  @override
  State<HiveLoader> createState() => _HiveLoaderState();
}

class _HiveLoaderState extends State<HiveLoader>
    with SingleTickerProviderStateMixin {
  late final AnimationController _clock = AnimationController(
    vsync: this,
    duration: const Duration(milliseconds: 1200),
  );

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    _run(_clock, context);
  }

  @override
  void dispose() {
    _clock.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final color = widget.color ?? HiveColors.of(context).accent;
    return Semantics(
      label: '正在加载',
      child: RepaintBoundary(
        child: CustomPaint(
          size: Size.square(widget.size),
          painter: _CombPainter(_clock, color),
        ),
      ),
    );
  }
}

/// 按钮里、行尾的小转圈：六边形描边，一段亮边绕着走。
class HexSpinner extends StatefulWidget {
  const HexSpinner({super.key, this.size = 16, this.strokeWidth, this.color});

  final double size;
  final double? strokeWidth;
  final Color? color;

  @override
  State<HexSpinner> createState() => _HexSpinnerState();
}

class _HexSpinnerState extends State<HexSpinner>
    with SingleTickerProviderStateMixin {
  late final AnimationController _clock = AnimationController(
    vsync: this,
    duration: const Duration(milliseconds: 1100),
  );

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    _run(_clock, context);
  }

  @override
  void dispose() {
    _clock.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final color = widget.color ?? HiveColors.of(context).accent;
    return Semantics(
      label: '正在加载',
      child: RepaintBoundary(
        child: CustomPaint(
          size: Size.square(widget.size),
          painter: _TracePainter(
            _clock,
            color,
            widget.strokeWidth ?? math.max(1.5, widget.size / 9),
          ),
        ),
      ),
    );
  }
}

/// 系统开了减弱动态效果时停在一帧上，不转。
void _run(AnimationController clock, BuildContext context) {
  if (MediaQuery.disableAnimationsOf(context)) {
    clock.stop();
    clock.value = 0.2;
  } else if (!clock.isAnimating) {
    clock.repeat();
  }
}

/// 尖顶朝上的六边形的第 [i] 个顶点（从正上方起顺时针），中心 [c]、外接圆半径 [r]。
Offset _corner(Offset c, double r, int i) {
  final a = math.pi / 3 * (i % 6) - math.pi / 2;
  return c + Offset(math.cos(a), math.sin(a)) * r;
}

/// 圆角六边形：每个角从两边各退 [round] 处用贝塞尔连过去，填充一次就成形，
/// 半透明的颜色不会因为填充和描边叠在一起出现一圈深色。
Path _roundHex(Offset c, double r, double round) {
  final path = Path();
  for (var i = 0; i < 6; i++) {
    final v = _corner(c, r, i);
    final from = Offset.lerp(v, _corner(c, r, i + 5), round / r)!;
    final to = Offset.lerp(v, _corner(c, r, i + 1), round / r)!;
    i == 0 ? path.moveTo(from.dx, from.dy) : path.lineTo(from.dx, from.dy);
    path.quadraticBezierTo(v.dx, v.dy, to.dx, to.dy);
  }
  return path..close();
}

class _CombPainter extends CustomPainter {
  _CombPainter(this.clock, this.color) : super(repaint: clock);

  final Animation<double> clock;
  final Color color;

  @override
  void paint(Canvas canvas, Size size) {
    // 七格按真实蜂巢排：横向放得下 3√3 个外接圆半径加两道缝
    const gapRatio = 0.2;
    final r = size.shortestSide / (3 * math.sqrt(3) + 2 * gapRatio);
    final step = math.sqrt(3) * r + gapRatio * r;
    final center = size.center(Offset.zero);
    final round = r * 0.22;
    final dim = color.withValues(alpha: 0.18);
    final paint = Paint();
    void cell(Offset c, Color fill) {
      canvas.drawPath(_roundHex(c, r, round), paint..color = fill);
    }

    cell(center, color.withValues(alpha: 0.28));
    final t = clock.value * 6;
    for (var i = 0; i < 6; i++) {
      // 从右上那格开始顺时针
      final a = math.pi / 3 * i - math.pi / 3;
      final c = center + Offset(math.cos(a), math.sin(a)) * step;
      // 亮过之后约两格的时间里慢慢暗下去
      final since = (t - i) % 6;
      final lit = math.max(0.0, 1 - since / 2.4);
      cell(c, Color.lerp(dim, color, Curves.easeOut.transform(lit))!);
    }
  }

  @override
  bool shouldRepaint(_CombPainter old) =>
      old.color != color || old.clock != clock;
}

class _TracePainter extends CustomPainter {
  _TracePainter(this.clock, this.color, this.width) : super(repaint: clock);

  final Animation<double> clock;
  final Color color;
  final double width;

  @override
  void paint(Canvas canvas, Size size) {
    final r = size.shortestSide / 2 - width / 2;
    final c = size.center(Offset.zero);
    final paint = Paint()
      ..style = PaintingStyle.stroke
      ..strokeWidth = width
      ..strokeJoin = StrokeJoin.round
      ..strokeCap = StrokeCap.round
      ..color = color.withValues(alpha: 0.2);
    final outline = Path()
      ..addPolygon([for (var i = 0; i < 6; i++) _corner(c, r, i)], true);
    canvas.drawPath(outline, paint);

    // 位置按边数算（0 到 6 是一圈），亮段占两条边。整段一笔画完再用扫描渐变
    // 由淡到亮：分几截画的话，每截的圆头叠在一起像一串珠子。
    Offset at(double s) {
      final i = s.floor();
      return Offset.lerp(_corner(c, r, i), _corner(c, r, i + 1), s - i)!;
    }

    final head = clock.value * 6 + 6;
    final tail = head - 2;
    final seg = Path();
    final start = at(tail);
    seg.moveTo(start.dx, start.dy);
    for (var v = tail.floor() + 1; v < head; v++) {
      final p = _corner(c, r, v);
      seg.lineTo(p.dx, p.dy);
    }
    final end = at(head);
    seg.lineTo(end.dx, end.dy);

    double angle(Offset p) => math.atan2(p.dy - c.dy, p.dx - c.dx);
    final from = angle(start);
    final f = ((angle(end) - from) % (2 * math.pi)) / (2 * math.pi);
    final clear = color.withValues(alpha: 0);
    paint
      ..color = color
      ..shader = SweepGradient(
        // 头上的圆头比亮段多出一点，要接着亮；尾巴后面的圆头转到接近一整圈，要透明
        colors: [clear, color, color, clear],
        stops: [0, f, (1 + f) / 2, (1 + f) / 2],
        transform: GradientRotation(from),
      ).createShader(Offset.zero & size);
    canvas.drawPath(seg, paint);
  }

  @override
  bool shouldRepaint(_TracePainter old) =>
      old.color != color || old.width != width || old.clock != clock;
}
