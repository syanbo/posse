import 'package:flutter/gestures.dart';
import 'package:flutter/material.dart';

/// 一组图片和视频左右翻看。翻走的那页会被 PageView 释放：视频停下来、图片的缩放不留着。
/// 图片放大后锁住翻页，单指拖动只移动图片，缩回原大才能再翻（[ZoomImage]）。
/// 第二根手指按下也锁住翻页：翻页 18 像素就认，双指捏合时常有一根先动过这个距离，
/// 不锁的话捏合会变成翻页。
class MediaPager extends StatefulWidget {
  const MediaPager({
    super.key,
    required this.count,
    required this.initialIndex,
    required this.onChanged,
    required this.builder,
  });

  final int count;
  final int initialIndex;
  final ValueChanged<int> onChanged;
  final IndexedWidgetBuilder builder;

  @override
  State<MediaPager> createState() => _MediaPagerState();
}

class _MediaPagerState extends State<MediaPager> {
  late final _controller = PageController(initialPage: widget.initialIndex);
  final _zoomed = ValueNotifier(false);
  var _pointers = 0;
  var _multi = false;

  void _down(PointerDownEvent _) {
    _pointers++;
    if (_pointers >= 2 && !_multi) setState(() => _multi = true);
  }

  void _up(PointerEvent _) {
    if (_pointers > 0) _pointers--;
    if (_pointers == 0 && _multi) setState(() => _multi = false);
  }

  @override
  void dispose() {
    _controller.dispose();
    _zoomed.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Listener(
      onPointerDown: _down,
      onPointerUp: _up,
      onPointerCancel: _up,
      child: ValueListenableBuilder(
        valueListenable: _zoomed,
        builder: (context, zoomed, _) => PageView.builder(
          controller: _controller,
          // 放大后翻页不再认手势：它的拖动 18 像素就认，比图片的平移先抢到
          physics: zoomed || _multi || widget.count <= 1
              ? const NeverScrollableScrollPhysics()
              : null,
          onPageChanged: (i) {
            _zoomed.value = false;
            widget.onChanged(i);
          },
          itemCount: widget.count,
          itemBuilder: (context, i) =>
              _PagerScope(zoomed: _zoomed, child: widget.builder(context, i)),
        ),
      ),
    );
  }
}

class _PagerScope extends InheritedWidget {
  const _PagerScope({required this.zoomed, required super.child});

  final ValueNotifier<bool> zoomed;

  @override
  bool updateShouldNotify(_PagerScope old) => old.zoomed != zoomed;
}

const _maxScale = 6.0;

/// 能双指放大的图片，放大后单指平移，画面始终铺满。
/// 不用 InteractiveViewer：它原大时也抢单指拖动（快划一下第一个事件就超过 36 像素），
/// 放在 [MediaPager] 里翻不了页。这里原大时只认双指，单指让给翻页。
class ZoomImage extends StatefulWidget {
  const ZoomImage({super.key, required this.child});

  final Widget child;

  @override
  State<ZoomImage> createState() => _ZoomImageState();
}

class _ZoomImageState extends State<ZoomImage> {
  var _scale = 1.0;
  var _offset = Offset.zero;
  Size _size = Size.zero;

  // 一次手势开始时的状态
  var _startScale = 1.0;
  var _startOffset = Offset.zero;
  var _startFocal = Offset.zero;
  var _active = false;

  bool get _zoomed => _scale > 1.01;

  void _start(ScaleStartDetails d) {
    _active = true;
    _startScale = _scale;
    _startOffset = _offset;
    _startFocal = d.localFocalPoint;
  }

  void _update(ScaleUpdateDetails d) {
    final scale = (_startScale * d.scale).clamp(1.0, _maxScale);
    // 手势开始时手指下的那一点跟着手指走
    final anchor = (_startFocal - _startOffset) / _startScale;
    final offset = d.localFocalPoint - anchor * scale;
    setState(() {
      _scale = scale;
      _offset = _clamp(offset, scale);
    });
    _report();
  }

  void _end(ScaleEndDetails d) {
    _active = false;
  }

  /// 画面不离开视口：放大后四边都不露底。
  Offset _clamp(Offset o, double scale) => Offset(
    o.dx.clamp(_size.width * (1 - scale), 0),
    o.dy.clamp(_size.height * (1 - scale), 0),
  );

  void _report() {
    final scope = context
        .getElementForInheritedWidgetOfExactType<_PagerScope>()
        ?.widget;
    if (scope is _PagerScope) scope.zoomed.value = _zoomed;
  }

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(
      builder: (context, constraints) {
        final size = constraints.biggest;
        if (size != _size) {
          _size = size;
          _offset = _clamp(_offset, _scale);
        }
        return RawGestureDetector(
          behavior: HitTestBehavior.opaque,
          gestures: {
            _ZoomRecognizer:
                GestureRecognizerFactoryWithHandlers<_ZoomRecognizer>(
                  () => _ZoomRecognizer(
                    debugOwner: this,
                    passive: () => !_zoomed && !_active,
                  ),
                  (r) => r
                    ..onStart = _start
                    ..onUpdate = _update
                    ..onEnd = _end,
                ),
          },
          child: ClipRect(
            child: Transform(
              transform: Matrix4.translationValues(_offset.dx, _offset.dy, 0)
                ..scaleByDouble(_scale, _scale, 1, 1),
              child: SizedBox.fromSize(
                size: size,
                child: Center(child: widget.child),
              ),
            ),
          ),
        );
      },
    );
  }
}

/// [passive] 时（原大、没在手势里）单指不认，让外面的翻页拿去；双指照常。
class _ZoomRecognizer extends ScaleGestureRecognizer {
  _ZoomRecognizer({super.debugOwner, required this.passive});

  final bool Function() passive;

  @override
  void resolve(GestureDisposition disposition) {
    if (disposition == GestureDisposition.accepted &&
        pointerCount < 2 &&
        passive()) {
      disposition = GestureDisposition.rejected;
    }
    super.resolve(disposition);
  }
}
