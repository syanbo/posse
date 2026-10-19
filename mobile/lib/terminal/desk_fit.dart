/// 电脑布局怎么把桌面格子放进手机屏幕。纯计算，不碰终端控件。
///
/// 比例照 Orca 的 computeFitScale：缩到视口宽度，接近铺满就当铺满。
/// 平移照 clampPan：内容超出才能动，没超出钉在左上。
const deskZoomStops = <double>[0.5, 0.75, 1, 1.25, 1.5, 2];

/// 桌面格子宽度相对视口的缩小比例，最大为 1。
double deskFitScale({
  required double viewportWidth,
  required double gridWidth,
}) {
  if (viewportWidth <= 0 || gridWidth <= 0) return 1;
  final scale = viewportWidth / gridWidth;
  if (scale >= 0.95) return 1;
  return scale > 1 ? 1 : scale;
}

/// 吸到最近的固定档。
double snapDeskZoom(double zoom) {
  var best = deskZoomStops.first;
  var bestDist = (zoom - best).abs();
  for (final stop in deskZoomStops.skip(1)) {
    final dist = (zoom - stop).abs();
    if (dist < bestDist) {
      best = stop;
      bestDist = dist;
    }
  }
  return best;
}

class DeskPan {
  const DeskPan(this.x, this.y);

  final double x;
  final double y;
}

/// 把平移夹在画面边缘。内容比视口小的那一轴钉在 0。
DeskPan clampDeskPan({
  required double panX,
  required double panY,
  required double contentWidth,
  required double contentHeight,
  required double viewportWidth,
  required double viewportHeight,
}) {
  final x = contentWidth > viewportWidth
      ? panX.clamp(viewportWidth - contentWidth, 0.0)
      : 0.0;
  final y = contentHeight > viewportHeight
      ? panY.clamp(viewportHeight - contentHeight, 0.0)
      : 0.0;
  return DeskPan(x, y);
}

class DeskDrag {
  const DeskDrag({
    required this.panX,
    required this.panY,
    required this.scrollDy,
  });

  final double panX;
  final double panY;

  /// 垂直方向没被平移吃掉的位移。手指往下为正，拿去滚回滚区。
  final double scrollDy;
}

/// 一根手指：水平只在画面比屏幕宽时平移；垂直先平移当前屏，到顶后剩下的才滚回滚区。
DeskDrag deskDrag({
  required double panX,
  required double panY,
  required double dx,
  required double dy,
  required double contentWidth,
  required double contentHeight,
  required double viewportWidth,
  required double viewportHeight,
}) {
  final clamped = clampDeskPan(
    panX: panX + dx,
    panY: panY + dy,
    contentWidth: contentWidth,
    contentHeight: contentHeight,
    viewportWidth: viewportWidth,
    viewportHeight: viewportHeight,
  );
  return DeskDrag(
    panX: clamped.x,
    panY: clamped.y,
    scrollDy: dy - (clamped.y - panY),
  );
}
