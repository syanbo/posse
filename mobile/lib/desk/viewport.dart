import 'dart:ui';

import 'events.dart';

/// 缩放 1 是整屏放进画面区，越大看得见的桌面范围越小。
const deskMinZoom = 1.0;
const deskMaxZoom = 4.0;

/// 画面按主屏比例放进视图后的矩形，留空在左右或上下。
class DeskFrame {
  const DeskFrame(this.left, this.top, this.width, this.height);

  final double left;
  final double top;
  final double width;
  final double height;

  double get right => left + width;
  double get bottom => top + height;
}

/// 看得见的那一块在整屏里的位置。pan 是这块的左上角，zoom 是 1 到 4。
class DeskViewport {
  DeskViewport({this.zoom = deskMinZoom, this.panX = 0, this.panY = 0});

  double zoom;
  double panX;
  double panY;

  void clamp() {
    if (zoom < deskMinZoom) zoom = deskMinZoom;
    if (zoom > deskMaxZoom) zoom = deskMaxZoom;
    final maxPan = zoom <= 1 ? 0.0 : 1 - 1 / zoom;
    panX = panX.clamp(0.0, maxPan);
    panY = panY.clamp(0.0, maxPan);
  }
}

/// 转屏或键盘把视图尺寸改了之后，原来在视图中心的那个桌面点还留在中心。
/// 只改 pan：zoom 是用户捏出来的，不跟着变。
void keepDesktopCenter({
  required DeskViewport viewport,
  required DeskFrame from,
  required DeskFrame to,
  required Size fromView,
  required Size toView,
}) {
  if (from.width <= 0 ||
      from.height <= 0 ||
      to.width <= 0 ||
      to.height <= 0 ||
      viewport.zoom <= 0) {
    return;
  }
  final zoom = viewport.zoom;
  final dx =
      (fromView.width / 2 - from.left) / (from.width * zoom) + viewport.panX;
  final dy =
      (fromView.height / 2 - from.top) / (from.height * zoom) + viewport.panY;
  viewport.panX = dx - (toView.width / 2 - to.left) / (to.width * zoom);
  viewport.panY = dy - (toView.height / 2 - to.top) / (to.height * zoom);
  viewport.clamp();
}

DeskFrame fitContain(double viewW, double viewH, double aspect) {
  if (viewW <= 0 || viewH <= 0 || aspect <= 0) {
    return DeskFrame(0, 0, viewW < 0 ? 0 : viewW, viewH < 0 ? 0 : viewH);
  }
  if (viewW / viewH > aspect) {
    final height = viewH;
    final width = height * aspect;
    return DeskFrame((viewW - width) / 2, 0, width, height);
  }
  final width = viewW;
  final height = width / aspect;
  return DeskFrame(0, (viewH - height) / 2, width, height);
}

/// 放大后的画面在视图里的矩形。超出视图的部分由外面裁掉。
Rect deskImageRect(DeskFrame fit, DeskViewport viewport) {
  final zoom = viewport.zoom;
  return Rect.fromLTWH(
    fit.left - viewport.panX * fit.width * zoom,
    fit.top - viewport.panY * fit.height * zoom,
    fit.width * zoom,
    fit.height * zoom,
  );
}

/// 视图坐标换成整屏 0–1。落在画面外（留空，或放大后还没盖到的地方）返回空。
DeskPoint? mapDesktop({
  required double x,
  required double y,
  required DeskFrame fit,
  required DeskViewport viewport,
}) {
  if (fit.width <= 0 || fit.height <= 0 || viewport.zoom <= 0) return null;
  final image = deskImageRect(fit, viewport);
  if (x < image.left || y < image.top || x > image.right || y > image.bottom) {
    return null;
  }
  return DeskPoint(
    ((x - image.left) / image.width).clamp(0.0, 1.0),
    ((y - image.top) / image.height).clamp(0.0, 1.0),
  );
}
