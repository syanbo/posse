import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'loading.dart';
import 'theme.dart';

// 闪屏分两段：系统闪屏（iOS LaunchScreen、安卓 launch_background 和 12 起的 SplashScreen）只有底色和居中的标志，
// 接着由这里在同一位置画同一个标志，再铺满蜂巢格。原生那张标志图由 test/splash_assets_test.dart 用 [PosseMarkPainter] 导出，
// 所以底色、标志尺寸和画法改了要重新导出。颜色写死不跟主题：系统闪屏在 App 读到主题设置之前就显示了。

const splashBackground = Color(0xFF1A1A19);

/// 原生闪屏图的边长（逻辑像素）。安卓 12 的闪屏图标固定 288dp、只露出中间直径 192dp 的圆，标志放得进这个圆。
const splashCanvas = 288.0;

/// 标志（六边形外缘的高度）在 [splashCanvas] 里的大小。
const splashMarkSize = 108.0;

/// 底部 slogan 图的尺寸和离屏幕底边的距离，照安卓 12 系统闪屏的品牌图（windowSplashScreenBrandingImage）：
/// 那里的位置由系统定，iOS 和这里跟着它放，三处叠在一起。
const splashSloganSize = Size(200, 80);
const splashSloganBottom = 60.0;
const splashSloganAsset = 'assets/splash_slogan.png';

/// slogan 本身，导出成图片给原生闪屏和 [PosseSplash] 共用：系统字体各端不同，用图片才对得上。
class SplashSlogan extends StatelessWidget {
  const SplashSlogan({super.key, this.fontFamily});

  final String? fontFamily;

  @override
  Widget build(BuildContext context) {
    return SizedBox.fromSize(
      size: splashSloganSize,
      child: Center(
        child: Text(
          'A posse of agents.',
          textDirection: TextDirection.ltr,
          style: TextStyle(
            fontFamily: fontFamily,
            fontSize: 13,
            letterSpacing: 0.6,
            color: const Color(0xFF8C8A85),
            decoration: TextDecoration.none,
            fontWeight: FontWeight.w400,
          ),
        ),
      ),
    );
  }
}

const _markFrom = Color(0xFFFFB02E);
const _markTo = Color(0xFFE5674C);

/// 应用图标里的那个标志：渐变描边的圆角六边形，里面一个 `>_`。画在 [size] 正中，高 [markSize]。
class PosseMarkPainter extends CustomPainter {
  const PosseMarkPainter({this.markSize = splashMarkSize});

  final double markSize;

  @override
  void paint(Canvas canvas, Size size) {
    final c = size.center(Offset.zero);
    // 比例量自 design/icon-art.png：描边约为中线半径的 0.19，提示符约 0.13
    final width = markSize / 2 / (1 + 0.19 / 2) * 0.19;
    final r = markSize / 2 - width / 2;
    final box = Rect.fromCircle(center: c, radius: markSize / 2);
    final paint = Paint()
      ..style = PaintingStyle.stroke
      ..strokeCap = StrokeCap.round
      ..strokeJoin = StrokeJoin.round
      ..shader = const LinearGradient(
        begin: Alignment.topLeft,
        end: Alignment.bottomRight,
        colors: [_markFrom, _markTo],
      ).createShader(box);
    canvas.drawPath(roundHex(c, r, r * 0.16), paint..strokeWidth = width);

    Offset at(double x, double y) => c + Offset(x, y) * r;
    final prompt = Path()
      ..moveTo(at(-0.43, -0.25).dx, at(-0.43, -0.25).dy)
      ..lineTo(at(-0.12, 0).dx, at(-0.12, 0).dy)
      ..lineTo(at(-0.43, 0.25).dx, at(-0.43, 0.25).dy)
      ..moveTo(at(0.06, 0.25).dx, at(0.06, 0.25).dy)
      ..lineTo(at(0.47, 0.25).dx, at(0.47, 0.25).dy);
    canvas.drawPath(prompt, paint..strokeWidth = r * 0.13);
  }

  @override
  bool shouldRepaint(PosseMarkPainter old) => old.markSize != markSize;
}

/// 启动时盖在首页上面的闪屏。[ready] 为真（本地状态读完）且至少显示了 [minimum] 后淡出，再调 [onDone]。
class PosseSplash extends StatefulWidget {
  const PosseSplash({
    super.key,
    required this.ready,
    required this.onDone,
    this.minimum = const Duration(milliseconds: 700),
  });

  final bool ready;
  final VoidCallback onDone;
  final Duration minimum;

  @override
  State<PosseSplash> createState() => _PosseSplashState();
}

class _PosseSplashState extends State<PosseSplash>
    with TickerProviderStateMixin {
  // 亮格陆续点亮，放完就停，不一直重绘
  late final AnimationController _intro = AnimationController(
    vsync: this,
    duration: const Duration(milliseconds: 600),
  );
  late final AnimationController _fade = AnimationController(
    vsync: this,
    duration: const Duration(milliseconds: 200),
    value: 1,
  );
  bool _minimumPassed = false;
  Timer? _timer;
  // 第一帧等 slogan 图解码好再上屏，这段时间原生闪屏还盖着；不然切过来的头一两帧底部是空的
  bool _holding = true;
  Timer? _holdLimit;
  bool _precaching = false;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.deferFirstFrame();
    _holdLimit = Timer(const Duration(milliseconds: 300), _release);
    _timer = Timer(widget.minimum, () {
      _minimumPassed = true;
      _maybeLeave();
    });
  }

  void _release() {
    if (!_holding) return;
    _holding = false;
    _holdLimit?.cancel();
    WidgetsBinding.instance.allowFirstFrame();
  }

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    if (!_precaching) {
      _precaching = true;
      precacheImage(
        const AssetImage(splashSloganAsset),
        context,
      ).whenComplete(_release);
    }
    if (reduceMotionOf(context)) {
      _intro.value = 1;
    } else if (_intro.value == 0 && !_intro.isAnimating) {
      _intro.forward();
    }
  }

  @override
  void didUpdateWidget(PosseSplash old) {
    super.didUpdateWidget(old);
    _maybeLeave();
  }

  void _maybeLeave() {
    if (!mounted || !widget.ready || !_minimumPassed) return;
    if (_fade.status == AnimationStatus.reverse) return;
    _fade.reverse().whenComplete(() {
      if (mounted) widget.onDone();
    });
  }

  @override
  void dispose() {
    _release();
    _timer?.cancel();
    _intro.dispose();
    _fade.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return AnnotatedRegion<SystemUiOverlayStyle>(
      value: SystemUiOverlayStyle.light,
      child: FadeTransition(
        opacity: _fade,
        child: ColoredBox(
          color: splashBackground,
          child: Stack(
            fit: StackFit.expand,
            children: [
              RepaintBoundary(child: CustomPaint(painter: _CombField(_intro))),
              const Center(
                child: CustomPaint(
                  size: Size.square(splashCanvas),
                  painter: PosseMarkPainter(),
                ),
              ),
              Positioned(
                left: 0,
                right: 0,
                bottom: splashSloganBottom,
                child: Center(
                  child: Image.asset(
                    splashSloganAsset,
                    width: splashSloganSize.width,
                    height: splashSloganSize.height,
                  ),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

/// 满屏蜂巢格：暗格铺到底，只在标志周围和 slogan 那一行留空；零星几格按 agent 状态色亮起（琥珀工作中、陶土待处理、绿色完成）。
class _CombField extends CustomPainter {
  _CombField(this.intro) : super(repaint: intro);

  final Animation<double> intro;

  static const _r = 19.0;
  static const _lit = [Color(0xFFF2AA2E), Color(0xFFD97757), Color(0xFF8DBB92)];

  @override
  void paint(Canvas canvas, Size size) {
    final t = intro.value;
    final center = size.center(Offset.zero);
    final clear = splashMarkSize * 0.95;
    // slogan 那行字的范围，格子和字叠在一起就看不清了
    final slogan = Rect.fromCenter(
      center: Offset(
        center.dx,
        size.height - splashSloganBottom - splashSloganSize.height / 2,
      ),
      width: splashSloganSize.width,
      height: _r * 2,
    );
    final dx = math.sqrt(3) * _r;
    final dy = 1.5 * _r;
    // 每次启动亮的格子一样：位置固定，看着是一张图而不是噪点
    final rand = math.Random(7);
    final dim = Paint()
      ..style = PaintingStyle.stroke
      ..strokeWidth = 1;
    final glow = Paint()
      ..style = PaintingStyle.stroke
      ..strokeWidth = 3
      ..maskFilter = const MaskFilter.blur(BlurStyle.normal, 4);
    final line = Paint()
      ..style = PaintingStyle.stroke
      ..strokeWidth = 1.6;
    final base = Curves.easeOut.transform((t / 0.3).clamp(0, 1));

    final rows = (size.height / dy).ceil() + 1;
    final cols = (size.width / dx).ceil() + 2;
    for (var row = 0; row < rows; row++) {
      for (var col = -1; col < cols; col++) {
        final c = Offset(col * dx + (row.isOdd ? dx / 2 : 0), row * dy);
        final roll = rand.nextDouble();
        final pick = rand.nextInt(_lit.length);
        final delay = rand.nextDouble();
        final d = (c - center).distance;
        if (d < clear || slogan.contains(c)) continue;
        final cell = roundHex(c, _r - 1.5, 4);
        canvas.drawPath(
          cell,
          dim..color = Colors.white.withValues(alpha: 0.07 * base),
        );
        // 离标志近的地方亮格密一点
        final chance = 0.15 + 0.13 * (1 - (d / size.longestSide).clamp(0, 1));
        if (roll > chance) continue;
        final start = 0.15 + delay * 0.6;
        final on = Curves.easeOut.transform(((t - start) / 0.25).clamp(0, 1));
        if (on == 0) continue;
        final color = _lit[pick];
        final inner = roundHex(c, _r * 0.78, 3.5);
        canvas.drawPath(
          inner,
          glow..color = color.withValues(alpha: 0.45 * on),
        );
        canvas.drawPath(inner, line..color = color.withValues(alpha: 0.9 * on));
      }
    }
    // 上下两端各 20% 渐隐到底色，网格不在屏幕边上硬切
    final edge = size.height * 0.2;
    for (final (top, from, to) in [
      (0.0, Alignment.topCenter, Alignment.bottomCenter),
      (size.height - edge, Alignment.bottomCenter, Alignment.topCenter),
    ]) {
      final band = Rect.fromLTWH(0, top, size.width, edge);
      canvas.drawRect(
        band,
        Paint()
          ..shader = LinearGradient(
            begin: from,
            end: to,
            colors: [splashBackground, splashBackground.withValues(alpha: 0)],
          ).createShader(band),
      );
    }
  }

  @override
  bool shouldRepaint(_CombField old) => old.intro != intro;
}
