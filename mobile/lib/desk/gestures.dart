import 'dart:math' as math;
import 'dart:ui';

import 'events.dart';
import 'viewport.dart';

const deskSlop = 12.0;
const deskLongPress = Duration(milliseconds: 300);
const deskMultiTap = Duration(milliseconds: 400);
const deskMultiTapPx = 24.0;

/// 间距变化或中心平移超过这么多逻辑像素才定性。慢动作里更早的抖动不算。
const deskClassifyPx = 10.0;

/// 按住拖到离视图边缘这个距离以内时，视口跟着挪。
const deskEdgePx = 36.0;

/// 手指贴着边缘时，视口每秒移动的整屏比例。
const deskEdgeSpeed = 0.5;

/// 三指横滑这么远（逻辑像素）算一次切空间。
const deskSpaceSwipePx = 60.0;

/// 触控板模式的指针加速：手指速度（逻辑像素/秒）在慢、快两档之间时，倍数从 1 线性升到最大。
const deskPointerSlow = 150.0;
const deskPointerFast = 1500.0;
const deskPointerMaxGain = 2.5;

/// 松手后滚轮速度的时间常数，约 0.7 秒收完。
const deskFlickTau = 0.18;
const deskFlickStop = 20.0;

class DeskSample {
  const DeskSample(this.span, this.centroid, this.time);

  final double span;
  final Offset centroid;
  final Duration time;
}

/// 间距变化比两指整体平移大就是缩放，否则是滚动。两者都还没超过
/// [deskClassifyPx] 时不定性，这一次定了就不再改。
DeskTwoFinger? classifyTwoFinger(List<DeskSample> samples) {
  if (samples.length < 2) return null;
  final first = samples.first;
  final last = samples.last;
  final span = (last.span - first.span).abs();
  final pan = (last.centroid - first.centroid).distance;
  if (span <= deskClassifyPx && pan <= deskClassifyPx) return null;
  return span > pan ? DeskTwoFinger.zoom : DeskTwoFinger.scroll;
}

enum DeskTwoFinger { scroll, zoom }

/// 手指在 [dt] 里走了 [distance] 逻辑像素时指针的倍数。
double deskPointerGain(double distance, Duration dt) {
  final seconds = dt.inMicroseconds / 1e6;
  if (seconds <= 0) return deskPointerMaxGain;
  final speed = distance / seconds;
  final t = ((speed - deskPointerSlow) / (deskPointerFast - deskPointerSlow))
      .clamp(0.0, 1.0);
  return 1 + (deskPointerMaxGain - 1) * t;
}

enum _One { pending, panning, pointing, dragging }

enum _Two { sampling, scroll, zoom }

class _Finger {
  _Finger(this.x, this.y, this.downAt) : originX = x, originY = y;

  double x;
  double y;
  double originX;
  double originY;
  Duration downAt;

  double distanceFromOrigin() =>
      (Offset(x, y) - Offset(originX, originY)).distance;
}

class _Flick {
  _Flick(this.velocity, this.at);

  Offset velocity;
  Duration at;
}

/// 看桌面的手势：直接点按，或把屏幕当触控板（[trackpad]）。不读时钟，时间由调用方传入。
///
/// 触控板模式下单指滑动按相对位移移动 [cursor]，点按、右键、拖动、滚动都落在指针上，
/// 放大后视口跟着指针走，不再单指平移。拖动是轻点后 [deskMultiTap] 内再按住滑动，
/// 不用长按：手指放上去停一下再滑是常见的移指针动作。
class DeskGestures {
  DeskGestures({
    required this.width,
    required this.height,
    required this.aspect,
    required this.onEvent,
    this.onArm,
  });

  double width;
  double height;
  double aspect;
  final void Function(DeskEvent event) onEvent;

  /// 长按满 300 ms、这一下即将变成拖动时调用一次。界面用来给轻触感。
  final void Function()? onArm;
  final viewport = DeskViewport();

  final Map<int, _Finger> _fingers = {};
  var _trackpad = false;

  /// 触控板模式下 Mac 指针在整屏上的位置。Mac 不报指针在哪，从画面中间开始。
  DeskPoint cursor = const DeskPoint(0.5, 0.5);
  Duration? _moveAt;
  var _tapDrag = false;
  _One? _one;
  int? _oneId;
  bool _armed = false;
  _Two? _two;
  var _samples = <DeskSample>[];
  double _zoom0 = 1;
  DeskPoint? _focal;
  Offset? _scrollFrom;
  _Flick? _flick;
  Duration? _edgeAt;
  var _ignore = false;

  /// 三指按下时三指的中点；横滑过 [deskSpaceSwipePx] 发一次切空间，之后这一下手势不再发。
  Offset? _three;
  var _threeDone = false;

  int _clicks = 0;
  Offset? _lastTapScreen;

  /// 最后一次单击在视图里的位置。
  Offset? get lastTap => _lastTapScreen;
  Duration _lastTapTime = Duration.zero;

  bool get trackpad => _trackpad;

  /// 手指按着时不换模式，免得一个手势前后两种算法。
  set trackpad(bool on) {
    if (_fingers.isEmpty) _trackpad = on;
  }

  /// 指针在视图里的位置。
  Offset get cursorView {
    final image = deskImageRect(fit, viewport);
    return Offset(
      image.left + cursor.x * image.width,
      image.top + cursor.y * image.height,
    );
  }

  bool get needsTick => _fingers.isNotEmpty || _flick != null;

  bool get dragging => _one == _One.dragging;

  int? get dragPointer => dragging ? _oneId : null;

  DeskFrame get fit => fitContain(width, height, aspect);

  void resize(double width, double height, double aspect) {
    this.width = width;
    this.height = height;
    this.aspect = aspect;
  }

  void down(int id, double x, double y, Duration time) {
    _fingers[id] = _Finger(x, y, time);
    if (_ignore) return;
    _flick = null;
    if (_fingers.length > 2) {
      _endTwo(time, still: false, abandon: true);
      _cancelOne();
      // 三指横滑切空间，四指以上不认
      if (_fingers.length == 3) {
        _three = _centroid();
        _threeDone = false;
      } else {
        _three = null;
        _ignore = true;
      }
      return;
    }
    if (_fingers.length == 1) {
      // 触控板模式整块视图都是触控板，留空的地方也认
      if (!_trackpad && _map(x, y) == null) return;
      _one = _One.pending;
      _oneId = id;
      _armed = false;
      _edgeAt = null;
      _moveAt = time;
      _tapDrag =
          _trackpad &&
          _lastTapScreen != null &&
          time - _lastTapTime <= deskMultiTap;
      return;
    }
    if (!_trackpad && !_anyOnPicture()) return;
    _cancelOne();
    _beginTwo(time);
  }

  void move(int id, double x, double y, Duration time) {
    final finger = _fingers[id];
    if (finger == null || _ignore) return;
    final dx = x - finger.x;
    final dy = y - finger.y;
    finger.x = x;
    finger.y = y;
    if (_three case final from?) {
      final d = _centroid() - from;
      if (!_threeDone &&
          d.dx.abs() >= deskSpaceSwipePx &&
          d.dx.abs() > 2 * d.dy.abs()) {
        _threeDone = true;
        // 和 Mac 触控板一样：手指往左划，换到右边那个空间
        onEvent(DeskSpaceEvent(next: d.dx < 0));
      }
      return;
    }
    if (_two != null) {
      _pushSample(time);
      return;
    }
    if (_oneId != id || _one == null) return;
    if (_one == _One.pending) {
      if (finger.distanceFromOrigin() <= deskSlop) return;
      if (_trackpad && _tapDrag) {
        _armHold();
        _startDrag(finger, time);
        _nudge(finger.x - finger.originX, finger.y - finger.originY, time);
        onEvent(DeskDragEvent(DeskDragPhase.move, cursor));
        return;
      } else if (_trackpad) {
        _one = _One.pointing;
        _nudge(finger.x - finger.originX, finger.y - finger.originY, time);
        onEvent(DeskPointEvent(cursor));
        return;
      } else if (_armed || time - finger.downAt >= deskLongPress) {
        _armHold();
        _startDrag(finger, time);
      } else {
        _one = _One.panning;
        // 越过阈值的那一下要把按下之后的位移算进去，不能只算最后一小段。
        _panBy(finger.x - finger.originX, finger.y - finger.originY);
        return;
      }
    }
    if (_one == _One.panning) {
      _panBy(dx, dy);
    } else if (_one == _One.pointing) {
      _nudge(dx, dy, time);
      onEvent(DeskPointEvent(cursor));
    } else if (_one == _One.dragging && _trackpad) {
      _nudge(dx, dy, time);
      onEvent(DeskDragEvent(DeskDragPhase.move, cursor));
    } else if (_one == _One.dragging) {
      onEvent(DeskDragEvent(DeskDragPhase.move, _dragPoint(finger)));
    }
  }

  void up(int id, double x, double y, Duration time) {
    final finger = _fingers[id];
    if (finger == null) return;
    finger.x = x;
    finger.y = y;
    if (_three != null) {
      _fingers.remove(id);
      if (_fingers.isEmpty) _three = null;
      return;
    }
    if (_two != null) {
      // 抬手的位置也算进这段移动，不然最后一下过了 10 像素还按轻点处理。
      _pushSample(time);
      _endTwo(time, still: _pairStill());
      _fingers.remove(id);
      if (_fingers.isEmpty) _ignore = false;
      return;
    }
    _fingers.remove(id);
    if (_oneId == id) _endOne(finger, time);
    if (_fingers.isEmpty) _ignore = false;
  }

  void cancel(int id, Duration time) {
    final finger = _fingers.remove(id);
    if (_three != null) {
      if (_fingers.isEmpty) _three = null;
      return;
    }
    if (_two != null) {
      _endTwo(time, still: false, abandon: true);
    } else if (finger != null && _oneId == id && _one == _One.dragging) {
      onEvent(DeskDragEvent(DeskDragPhase.up, _dragPoint(finger)));
      _clearOne();
    } else if (_oneId == id) {
      _clearOne();
    }
    if (_fingers.isEmpty) _ignore = false;
  }

  /// 长按计时、边缘跟手、松手后的惯性。视图没有新的指针事件时也要调用。
  void advance(Duration time) {
    if (_one == _One.pending && !_armed && !_trackpad) {
      final finger = _fingers[_oneId];
      if (finger != null && time - finger.downAt >= deskLongPress) {
        _armHold();
      }
    }
    if (_one == _One.dragging && !_trackpad) {
      final finger = _fingers[_oneId];
      if (finger != null && _edgePan(finger, time)) {
        onEvent(DeskDragEvent(DeskDragPhase.move, _dragPoint(finger)));
      }
    }
    _tickFlick(time);
  }

  void _beginTwo(Duration time) {
    _two = _Two.sampling;
    _samples = [];
    _zoom0 = viewport.zoom;
    _scrollFrom = null;
    _focal = null;
    _pushSample(time);
    final start = _samples.first;
    _focal = _map(start.centroid.dx, start.centroid.dy);
  }

  void _pushSample(Duration time) {
    final pts = _fingers.values.take(2).toList();
    if (pts.length < 2 || _two == null) return;
    final a = Offset(pts[0].x, pts[0].y);
    final b = Offset(pts[1].x, pts[1].y);
    _samples.add(DeskSample((a - b).distance, (a + b) / 2, time));
    if (_two == _Two.sampling && classifyTwoFinger(_samples) != null) {
      _lockTwo();
      return;
    }
    if (_two == _Two.scroll) _emitScrollStep();
    if (_two == _Two.zoom) _applyZoom();
  }

  void _lockTwo() {
    final kind = classifyTwoFinger(_samples);
    if (kind == null) return;
    _two = kind == DeskTwoFinger.zoom ? _Two.zoom : _Two.scroll;
    if (_two == _Two.zoom) {
      _applyZoom();
    } else {
      _emitScrollFromStart();
    }
  }

  void _applyZoom() {
    if (_samples.isEmpty) return;
    final base = _samples.first.span < 1 ? 1.0 : _samples.first.span;
    final span = _samples.last.span;
    viewport.zoom = (_zoom0 * (span / base)).clamp(deskMinZoom, deskMaxZoom);
    final focal = _focal;
    final c = _samples.last.centroid;
    final frame = fit;
    if (focal != null && frame.width > 0 && frame.height > 0) {
      viewport.panX =
          focal.x - (c.dx - frame.left) / (frame.width * viewport.zoom);
      viewport.panY =
          focal.y - (c.dy - frame.top) / (frame.height * viewport.zoom);
    }
    viewport.clamp();
  }

  void _emitScrollFromStart() {
    if (_samples.length < 2) return;
    final a = _samples.first.centroid;
    final b = _samples.last.centroid;
    final at = _trackpad ? cursor : _map(a.dx, a.dy);
    if (at != null) onEvent(DeskPointEvent(at));
    _scrollFrom = b;
    final d = b - a;
    if (d.distance == 0) return;
    onEvent(DeskScrollEvent(d.dx, d.dy, inertia: false));
  }

  void _emitScrollStep() {
    final c = _samples.last.centroid;
    final from = _scrollFrom ?? c;
    _scrollFrom = c;
    final d = c - from;
    if (d.distance == 0) return;
    onEvent(DeskScrollEvent(d.dx, d.dy, inertia: false));
  }

  void _endTwo(Duration time, {required bool still, bool abandon = false}) {
    // 还没到 10 像素就抬手：两指都在 slop 里才是右键，再大一点的晃动直接丢掉。
    if (!abandon && _two == _Two.sampling && still) _rightClick();
    if (!abandon && _two == _Two.scroll) _startFlick(time);
    _two = null;
    _samples = [];
    _scrollFrom = null;
  }

  void _rightClick() {
    final pts = _fingers.values.take(2).toList();
    if (pts.isEmpty) return;
    var x = 0.0, y = 0.0;
    for (final finger in pts) {
      x += finger.x;
      y += finger.y;
    }
    final point = _trackpad ? cursor : _map(x / pts.length, y / pts.length);
    if (point == null) return;
    onEvent(DeskRightClickEvent(point));
  }

  void _startFlick(Duration time) {
    if (_samples.length < 2) return;
    final last = _samples.last;
    DeskSample? early;
    for (final sample in _samples) {
      if (last.time - sample.time <= const Duration(milliseconds: 100)) {
        early ??= sample;
      }
    }
    early ??= _samples[_samples.length - 2];
    final dt = (last.time - early.time).inMicroseconds / 1e6;
    if (dt <= 0.001) return;
    final velocity = (last.centroid - early.centroid) / dt;
    if (velocity.distance < deskFlickStop) return;
    _flick = _Flick(velocity, time);
  }

  void _tickFlick(Duration time) {
    final flick = _flick;
    if (flick == null) return;
    var dt = (time - flick.at).inMicroseconds / 1e6;
    if (dt <= 0) return;
    if (dt > 0.05) dt = 0.05;
    final step = flick.velocity * dt;
    onEvent(DeskScrollEvent(step.dx, step.dy, inertia: true));
    flick.velocity *= math.exp(-dt / deskFlickTau);
    flick.at = time;
    if (flick.velocity.distance < deskFlickStop) _flick = null;
  }

  void _endOne(_Finger finger, Duration time) {
    if (_one == _One.dragging) {
      onEvent(DeskDragEvent(DeskDragPhase.up, _dragPoint(finger)));
    } else if (_one == _One.pending &&
        finger.distanceFromOrigin() <= deskSlop) {
      _tap(finger.x, finger.y, time);
    }
    _clearOne();
  }

  void _tap(double x, double y, Duration time) {
    final point = _trackpad ? cursor : _map(x, y);
    if (point == null) return;
    final screen = _trackpad ? cursorView : Offset(x, y);
    final last = _lastTapScreen;
    final near =
        last != null &&
        time - _lastTapTime <= deskMultiTap &&
        (screen - last).distance <= deskMultiTapPx;
    _clicks = near ? _clicks + 1 : 1;
    _lastTapScreen = screen;
    _lastTapTime = time;
    onEvent(DeskTapEvent(point, _clicks));
  }

  void _armHold() {
    if (_armed) return;
    _armed = true;
    onArm?.call();
  }

  void _startDrag(_Finger finger, Duration time) {
    _one = _One.dragging;
    _edgeAt = time;
    // 越过 slop 时手指已经离开原点。按下记落下的位置，这一下的移动另发。
    onEvent(
      DeskDragEvent(
        DeskDragPhase.down,
        _trackpad ? cursor : _pointAt(finger.originX, finger.originY),
      ),
    );
  }

  Offset _centroid() {
    var x = 0.0, y = 0.0;
    for (final f in _fingers.values) {
      x += f.x;
      y += f.y;
    }
    final n = _fingers.isEmpty ? 1 : _fingers.length;
    return Offset(x / n, y / n);
  }

  void _cancelOne() {
    final finger = _fingers[_oneId];
    if (_one == _One.dragging && finger != null) {
      onEvent(DeskDragEvent(DeskDragPhase.up, _dragPoint(finger)));
    }
    _clearOne();
  }

  void _clearOne() {
    _tapDrag = false;
    _one = null;
    _oneId = null;
    _armed = false;
    _edgeAt = null;
  }

  void _panBy(double dx, double dy) {
    final frame = fit;
    if (frame.width <= 0 || frame.height <= 0) return;
    final zoom = viewport.zoom;
    viewport.panX -= dx / (frame.width * zoom);
    viewport.panY -= dy / (frame.height * zoom);
    viewport.clamp();
  }

  /// 触控板模式：手指移动 (dx, dy) 逻辑像素，指针按速度加速后跟着走。
  /// 放大时按看得见的画面换算，指针在屏幕上走的距离和缩放无关。
  void _nudge(double dx, double dy, Duration time) {
    final frame = fit;
    if (frame.width <= 0 || frame.height <= 0) return;
    final prev = _moveAt ?? time;
    _moveAt = time;
    final gain = deskPointerGain(Offset(dx, dy).distance, time - prev);
    final scale = gain / viewport.zoom;
    cursor = DeskPoint(
      (cursor.x + dx * scale / frame.width).clamp(0.0, 1.0),
      (cursor.y + dy * scale / frame.height).clamp(0.0, 1.0),
    );
    _followCursor();
  }

  /// 放大时指针离视图边缘不到 [deskEdgePx] 就挪视口，让指针一直看得见。
  void _followCursor() {
    final frame = fit;
    final zoom = viewport.zoom;
    if (zoom <= deskMinZoom || frame.width <= 0 || frame.height <= 0) return;
    final at = cursorView;
    viewport.panX += _overshoot(at.dx, width) / (frame.width * zoom);
    viewport.panY += _overshoot(at.dy, height) / (frame.height * zoom);
    viewport.clamp();
  }

  double _overshoot(double pos, double limit) {
    if (limit <= deskEdgePx * 2) return 0;
    if (pos < deskEdgePx) return pos - deskEdgePx;
    if (pos > limit - deskEdgePx) return pos - (limit - deskEdgePx);
    return 0;
  }

  bool _edgePan(_Finger finger, Duration time) {
    final prev = _edgeAt ?? time;
    _edgeAt = time;
    var dt = (time - prev).inMicroseconds / 1e6;
    if (dt <= 0) return false;
    if (dt > 0.05) dt = 0.05;
    final vx = _edgePush(finger.x, width);
    final vy = _edgePush(finger.y, height);
    if (vx == 0 && vy == 0) return false;
    final beforeX = viewport.panX;
    final beforeY = viewport.panY;
    viewport.panX += vx * deskEdgeSpeed * dt;
    viewport.panY += vy * deskEdgeSpeed * dt;
    viewport.clamp();
    return viewport.panX != beforeX || viewport.panY != beforeY;
  }

  double _edgePush(double pos, double limit) {
    if (limit <= deskEdgePx * 2) return 0;
    if (pos < deskEdgePx) return -((deskEdgePx - pos) / deskEdgePx);
    if (pos > limit - deskEdgePx) {
      return (pos - (limit - deskEdgePx)) / deskEdgePx;
    }
    return 0;
  }

  DeskPoint _dragPoint(_Finger finger) =>
      _trackpad ? cursor : _pointAt(finger.x, finger.y);

  DeskPoint _pointAt(double x, double y) {
    final mapped = _map(x, y);
    if (mapped != null) return mapped;
    final image = deskImageRect(fit, viewport);
    return _map(
          x.clamp(image.left, image.right),
          y.clamp(image.top, image.bottom),
        ) ??
        const DeskPoint(0, 0);
  }

  DeskPoint? _map(double x, double y) =>
      mapDesktop(x: x, y: y, fit: fit, viewport: viewport);

  bool _anyOnPicture() => _fingers.values.any((f) => _map(f.x, f.y) != null);

  bool _pairStill() =>
      _fingers.values.every((f) => f.distanceFromOrigin() <= deskSlop);
}
