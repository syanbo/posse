import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/desk/events.dart';
import 'package:posse_mobile/desk/gestures.dart';
import 'package:posse_mobile/desk/viewport.dart';

void main() {
  test('classify waits until the movement passes 10 px', () {
    expect(
      classifyTwoFinger([
        for (var i = 0; i < 30; i++)
          DeskSample(
            40 + i * 0.3,
            Offset(i * 0.3, 0),
            Duration(milliseconds: i),
          ),
      ]),
      isNull,
    );
    expect(
      classifyTwoFinger([
        const DeskSample(40, Offset.zero, Duration.zero),
        const DeskSample(51, Offset(4, 0), Duration(milliseconds: 80)),
      ]),
      DeskTwoFinger.zoom,
    );
    expect(
      classifyTwoFinger([
        const DeskSample(40, Offset.zero, Duration.zero),
        const DeskSample(44, Offset(11, 0), Duration(milliseconds: 80)),
      ]),
      DeskTwoFinger.scroll,
    );
    // 间距变化和平移一样大时算滚动。
    expect(
      classifyTwoFinger([
        const DeskSample(40, Offset.zero, Duration.zero),
        const DeskSample(52, Offset(12, 0), Duration(milliseconds: 80)),
      ]),
      DeskTwoFinger.scroll,
    );
  });

  test('panning keeps the desktop point under the finger', () {
    final g = _gestures();
    g.viewport.zoom = 2;
    g.down(1, 200, 100, Duration.zero);
    g.move(1, 100, 100, const Duration(milliseconds: 50));
    expect(g.viewport.panX, closeTo(0.125, 1e-9));
    expect(g.viewport.panY, closeTo(0, 1e-9));
    final under = mapDesktop(x: 100, y: 100, fit: g.fit, viewport: g.viewport)!;
    expect(under.x, closeTo(0.25, 1e-9));
    expect(under.y, closeTo(0.25, 1e-9));
  });

  test('a drag at zoom 1 does not pan and is not a tap', () {
    final g = _gestures();
    g.down(1, 200, 100, Duration.zero);
    g.move(1, 100, 100, const Duration(milliseconds: 50));
    g.up(1, 100, 100, const Duration(milliseconds: 80));
    expect(g.viewport.panX, 0);
    expect(g.viewport.panY, 0);
    expect(g.events, isEmpty);
  });

  test('letterbox touches are dropped and a tap on the picture counts', () {
    final g = _gestures(width: 200, height: 400);
    g.down(1, 100, 20, Duration.zero);
    g.up(1, 100, 20, const Duration(milliseconds: 40));
    expect(g.events, isEmpty);

    g.down(1, 100, 200, const Duration(milliseconds: 80));
    g.up(1, 100, 200, const Duration(milliseconds: 100));
    final tap = g.events.single as DeskTapEvent;
    expect(tap.clicks, 1);
    expect(tap.at.x, closeTo(0.5, 1e-9));
    expect(tap.at.y, closeTo(0.5, 1e-9));
  });

  test('taps within 400 ms and 24 px accumulate click state', () {
    final g = _gestures();
    _tap(g, 100, 100, 0);
    _tap(g, 100, 100, 200);
    _tap(g, 100, 100, 400);
    expect(g.events.map((e) => (e as DeskTapEvent).clicks), [1, 2, 3]);

    final boundary = _gestures();
    _tap(boundary, 100, 100, 0);
    _tap(boundary, 124, 100, 400);
    expect((boundary.events.last as DeskTapEvent).clicks, 2);

    final late = _gestures();
    _tap(late, 100, 100, 0);
    _tap(late, 100, 100, 401);
    expect((late.events.last as DeskTapEvent).clicks, 1);

    final far = _gestures();
    _tap(far, 100, 100, 0);
    _tap(far, 125, 100, 100);
    expect((far.events.last as DeskTapEvent).clicks, 1);
  });

  test('a two-finger tap is a right click at the centroid', () {
    final g = _gestures();
    g.down(1, 100, 100, Duration.zero);
    g.down(2, 140, 120, Duration.zero);
    g.up(2, 140, 120, const Duration(milliseconds: 40));
    g.up(1, 100, 100, const Duration(milliseconds: 50));
    final click = g.events.single as DeskRightClickEvent;
    expect(click.at.x, closeTo(0.3, 1e-9));
    expect(click.at.y, closeTo(0.55, 1e-9));
  });

  test('a long-press drag presses where the finger landed', () {
    final g = _gestures();
    // 落下在 (0.5, 0.4)。停够长按后先只越过 slop，再拖到 (0.7, 0.5)。
    g.down(1, 200, 80, Duration.zero);
    g.advance(const Duration(milliseconds: 500));
    g.move(1, 213, 80, const Duration(milliseconds: 520));
    g.move(1, 280, 100, const Duration(milliseconds: 560));
    g.up(1, 280, 100, const Duration(milliseconds: 600));
    final events = [for (final event in g.events) event as DeskDragEvent];
    expect(events.map((event) => event.phase), [
      DeskDragPhase.down,
      DeskDragPhase.move,
      DeskDragPhase.move,
      DeskDragPhase.up,
    ]);
    expect(events[0].at.x, closeTo(0.5, 1e-9));
    expect(events[0].at.y, closeTo(0.4, 1e-9));
    expect(events[1].at.x, closeTo(213 / 400, 1e-9));
    expect(events[1].at.y, closeTo(0.4, 1e-9));
    expect(events[2].at.x, closeTo(0.7, 1e-9));
    expect(events[2].at.y, closeTo(0.5, 1e-9));
    expect(events[3].at.x, closeTo(0.7, 1e-9));
    expect(events[3].at.y, closeTo(0.5, 1e-9));
  });

  test('holding 300 ms then moving drags, and 299 ms pans', () {
    final drag = _gestures();
    drag.down(1, 200, 100, Duration.zero);
    drag.advance(const Duration(milliseconds: 300));
    drag.move(1, 230, 100, const Duration(milliseconds: 300));
    drag.move(1, 260, 100, const Duration(milliseconds: 340));
    drag.up(1, 260, 100, const Duration(milliseconds: 360));
    expect(drag.viewport.panX, 0);
    expect(drag.events.map((e) => (e as DeskDragEvent).phase), [
      DeskDragPhase.down,
      DeskDragPhase.move,
      DeskDragPhase.move,
      DeskDragPhase.up,
    ]);

    final pan = _gestures();
    pan.viewport.zoom = 2;
    pan.viewport.panX = 0.25;
    pan.down(1, 200, 100, Duration.zero);
    pan.move(1, 230, 100, const Duration(milliseconds: 299));
    expect(pan.events, isEmpty);
    expect(pan.viewport.panX, closeTo(0.25 - 30 / 800, 1e-9));
  });

  test('a drag at the edge pans the viewport and the point follows', () {
    final g = _gestures();
    g.viewport.zoom = 2;
    g.down(1, 200, 100, Duration.zero);
    g.advance(const Duration(milliseconds: 300));
    g.move(1, 390, 100, const Duration(milliseconds: 310));
    final down = g.events.first as DeskDragEvent;
    g.advance(const Duration(milliseconds: 360));
    expect(g.viewport.panX, greaterThan(0));
    final move = g.events.last as DeskDragEvent;
    expect(move.phase, DeskDragPhase.move);
    expect(move.at.x, greaterThan(down.at.x));
  });

  test('the first 10 samples lock a pinch, and later motion stays a pinch', () {
    final g = _gestures();
    g.down(1, 180, 100, Duration.zero);
    g.down(2, 220, 100, Duration.zero);
    for (var i = 0; i < 4; i++) {
      final t = Duration(milliseconds: 10 * (i + 1));
      g.move(1, 180 - 4.0 * (i + 1), 100, t);
      g.move(2, 220 + 4.0 * (i + 1), 100, t);
    }
    g.move(1, 160, 100, const Duration(milliseconds: 50));
    expect(g.viewport.zoom, closeTo(1.9, 1e-9));
    expect(g.events, isEmpty);
    final focal = mapDesktop(x: 198, y: 100, fit: g.fit, viewport: g.viewport)!;
    expect(focal.x, closeTo(0.5, 1e-9));
    expect(focal.y, closeTo(0.5, 1e-9));

    g.move(1, 200, 100, const Duration(milliseconds: 60));
    g.move(2, 276, 100, const Duration(milliseconds: 70));
    expect(g.events.whereType<DeskScrollEvent>(), isEmpty);
    expect(g.viewport.zoom, closeTo(1.9, 1e-9));
    final stayed = mapDesktop(
      x: 238,
      y: 100,
      fit: g.fit,
      viewport: g.viewport,
    )!;
    expect(stayed.x, closeTo(0.5, 1e-9));
    expect(stayed.y, closeTo(0.5, 1e-9));
  });

  test('a pinch clamps at 4 and at 1', () {
    final open = _gestures();
    open.down(1, 180, 100, Duration.zero);
    open.down(2, 220, 100, Duration.zero);
    for (var i = 0; i < 8; i++) {
      open.move(1, 180, 100, Duration(milliseconds: i + 1));
    }
    open.move(2, 400, 100, const Duration(milliseconds: 20));
    open.move(1, 80, 100, const Duration(milliseconds: 30));
    expect(open.viewport.zoom, deskMaxZoom);

    final shut = _gestures();
    shut.viewport.zoom = 2;
    shut.down(1, 150, 100, Duration.zero);
    shut.down(2, 250, 100, Duration.zero);
    for (var i = 0; i < 8; i++) {
      shut.move(1, 150, 100, Duration(milliseconds: i + 1));
    }
    shut.move(2, 170, 100, const Duration(milliseconds: 20));
    shut.move(1, 150, 100, const Duration(milliseconds: 30));
    expect(shut.viewport.zoom, deskMinZoom);
    expect(shut.viewport.panX, 0);
  });

  test(
    'a parallel two-finger drag scrolls, then flicks with a decaying tail',
    () {
      final g = _gestures();
      g.down(1, 100, 100, Duration.zero);
      g.down(2, 140, 100, Duration.zero);
      // 两指一起走。单指先动的那几像素不到 10，不会被当成捏合。
      for (var i = 1; i <= 20; i++) {
        final x = i * 5.0;
        final t = Duration(milliseconds: 4 * i);
        g.move(1, 100 + x, 100, t);
        g.move(2, 140 + x, 100, t);
      }
      expect(g.viewport.zoom, 1);
      // 滚动之前先把指针移到两指按下时的中点，滚动落在手指下面的窗口
      final point = g.events.first as DeskPointEvent;
      final expected = mapDesktop(
        x: 120,
        y: 100,
        fit: g.fit,
        viewport: g.viewport,
      )!;
      expect(point.at.x, closeTo(expected.x, 1e-9));
      expect(point.at.y, closeTo(expected.y, 1e-9));
      expect(g.events[1], isA<DeskScrollEvent>());
      expect(g.events.whereType<DeskPointEvent>(), hasLength(1));
      final scrolls = g.events.whereType<DeskScrollEvent>().toList();
      expect(scrolls, isNotEmpty);
      expect(scrolls.every((event) => !event.inertia), isTrue);
      expect(
        scrolls.fold<double>(0, (sum, event) => sum + event.dx),
        closeTo(100, 1e-9),
      );
      expect(scrolls.every((event) => event.dy == 0), isTrue);

      g.up(1, 200, 100, const Duration(milliseconds: 80));
      g.up(2, 240, 100, const Duration(milliseconds: 80));
      final steps = <double>[];
      var time = const Duration(milliseconds: 80);
      for (var i = 0; i < 80 && g.needsTick; i++) {
        time += const Duration(milliseconds: 16);
        g.advance(time);
        final last = g.events.last;
        if (last is DeskScrollEvent && last.inertia) steps.add(last.dx);
      }
      expect(steps.first, closeTo(20, 1e-6));
      expect(steps[1], lessThan(steps[0]));
      expect(steps.last, greaterThan(0));
      expect(g.needsTick, isFalse);
      expect(g.events.whereType<DeskScrollEvent>().last.inertia, isTrue);
    },
  );

  test('a slow pinch ignores noise under 10 px and then zooms', () {
    final g = _gestures();
    g.down(1, 150, 100, Duration.zero);
    g.down(2, 250, 100, Duration.zero);
    for (var i = 1; i <= 9; i++) {
      g.move(1, 150, 100, Duration(milliseconds: 20 * i));
      g.move(2, 250 + i.toDouble(), 100, Duration(milliseconds: 20 * i));
    }
    expect(g.viewport.zoom, 1);
    expect(g.events, isEmpty);

    g.move(2, 261, 100, const Duration(milliseconds: 200));
    expect(g.viewport.zoom, closeTo(1.11, 1e-9));
    expect(g.events.whereType<DeskScrollEvent>(), isEmpty);
  });

  test('a slow two-finger drag ignores noise under 10 px and then scrolls', () {
    final g = _gestures();
    g.down(1, 100, 100, Duration.zero);
    g.down(2, 160, 100, Duration.zero);
    for (var i = 1; i <= 9; i++) {
      g.move(1, 100 + i.toDouble(), 100, Duration(milliseconds: 20 * i));
      g.move(2, 160 + i.toDouble(), 100, Duration(milliseconds: 20 * i));
    }
    expect(g.viewport.zoom, 1);
    expect(g.events, isEmpty);

    g.move(1, 112, 100, const Duration(milliseconds: 200));
    g.move(2, 172, 100, const Duration(milliseconds: 210));
    expect(g.viewport.zoom, 1);
    final scrolls = g.events.whereType<DeskScrollEvent>().toList();
    expect(scrolls, isNotEmpty);
    expect(scrolls.every((event) => !event.inertia), isTrue);
    expect(
      scrolls.fold<double>(0, (sum, event) => sum + event.dx),
      closeTo(12, 1e-9),
    );
  });

  test('a pinch past 10 px zooms before the fingers lift, and a third finger abandons', () {
    final pinch = _gestures();
    pinch.down(1, 180, 100, Duration.zero);
    pinch.down(2, 220, 100, Duration.zero);
    pinch.move(1, 100, 100, const Duration(milliseconds: 20));
    pinch.up(1, 100, 100, const Duration(milliseconds: 30));
    pinch.up(2, 220, 100, const Duration(milliseconds: 30));
    expect(pinch.viewport.zoom, closeTo(3, 1e-9));
    expect(pinch.events.whereType<DeskScrollEvent>(), isEmpty);

    final abandoned = _gestures();
    abandoned.down(1, 100, 100, Duration.zero);
    abandoned.down(2, 160, 100, Duration.zero);
    abandoned.down(3, 200, 100, const Duration(milliseconds: 20));
    abandoned.up(1, 100, 100, const Duration(milliseconds: 40));
    abandoned.up(2, 160, 100, const Duration(milliseconds: 40));
    abandoned.up(3, 200, 100, const Duration(milliseconds: 40));
    abandoned.advance(const Duration(milliseconds: 800));
    expect(abandoned.events, isEmpty);
    expect(abandoned.viewport.zoom, 1);

    // 过了 slop，但间距和中心都还没到 10 像素：不是右键，也不滚动。
    final wobble = _gestures();
    wobble.down(1, 100, 100, Duration.zero);
    wobble.down(2, 180, 100, Duration.zero);
    wobble.move(1, 100, 113, const Duration(milliseconds: 20));
    wobble.up(1, 100, 113, const Duration(milliseconds: 30));
    wobble.up(2, 180, 100, const Duration(milliseconds: 30));
    expect(wobble.events, isEmpty);
    expect(wobble.viewport.zoom, 1);
  });

  test('a three-finger swipe switches spaces once, like the trackpad', () {
    final g = _gestures();
    g.down(1, 100, 100, Duration.zero);
    g.down(2, 140, 100, Duration.zero);
    g.down(3, 180, 100, const Duration(milliseconds: 5));
    for (var i = 1; i <= 20; i++) {
      final t = Duration(milliseconds: 10 + 8 * i);
      for (final id in [1, 2, 3]) {
        g.move(id, 60.0 + id * 40 - i * 8, 100 + i * 0.5, t);
      }
    }
    // 手指往左划：右边那个空间，一下手势只发一次，不当成滚动、缩放、右键
    expect(g.events.whereType<DeskSpaceEvent>().map((e) => e.next), [true]);
    expect(g.events.whereType<DeskScrollEvent>(), isEmpty);
    expect(g.events.whereType<DeskRightClickEvent>(), isEmpty);
    expect(g.viewport.zoom, 1);
    for (final id in [1, 2, 3]) {
      g.up(id, 0, 100, const Duration(milliseconds: 300));
    }

    // 往右划是左边的空间；竖着滑不算
    final right = _gestures();
    final vertical = _gestures();
    for (final h in [right, vertical]) {
      h.down(1, 100, 100, Duration.zero);
      h.down(2, 140, 100, Duration.zero);
      h.down(3, 180, 100, Duration.zero);
    }
    for (final id in [1, 2, 3]) {
      right.move(
        id,
        60.0 + id * 40 + 80,
        100,
        const Duration(milliseconds: 50),
      );
      vertical.move(id, 60.0 + id * 40, 200, const Duration(milliseconds: 50));
    }
    expect(right.events.whereType<DeskSpaceEvent>().map((e) => e.next), [
      false,
    ]);
    expect(vertical.events.whereType<DeskSpaceEvent>(), isEmpty);

    // 四指不认
    final four = _gestures();
    for (final id in [1, 2, 3, 4]) {
      four.down(id, 100.0 + id * 30, 100, Duration.zero);
    }
    for (final id in [1, 2, 3, 4]) {
      four.move(id, id * 30.0, 100, const Duration(milliseconds: 50));
    }
    expect(four.events, isEmpty);
  });

  test(
    'trackpad: a slow slide moves the cursor 1:1 and a tap clicks there',
    () {
      final g = _gestures()..gestures.trackpad = true;
      g.down(1, 100, 100, Duration.zero);
      g.move(1, 140, 100, const Duration(seconds: 1));
      g.up(1, 140, 100, const Duration(milliseconds: 1020));
      final point = g.events.single as DeskPointEvent;
      expect(point.at.x, closeTo(0.6, 1e-9));
      expect(point.at.y, closeTo(0.5, 1e-9));
      // 点哪都按在指针上
      _tap(g, 10, 10, 2000);
      final tap = g.events.last as DeskTapEvent;
      expect(tap.at.x, closeTo(0.6, 1e-9));
      expect(tap.at.y, closeTo(0.5, 1e-9));
      _tap(g, 300, 150, 2200);
      expect((g.events.last as DeskTapEvent).clicks, 2);
    },
  );

  test('trackpad: a fast slide is accelerated and clamped to the screen', () {
    expect(deskPointerGain(10, const Duration(seconds: 1)), 1);
    expect(deskPointerGain(40, const Duration(milliseconds: 20)), 2.5);
    final g = _gestures()..gestures.trackpad = true;
    g.down(1, 100, 100, Duration.zero);
    g.move(1, 140, 100, const Duration(milliseconds: 20));
    expect(g.gestures.cursor.x, closeTo(0.75, 1e-9));
    g.move(1, 400, 100, const Duration(milliseconds: 40));
    expect(g.gestures.cursor.x, 1);
  });

  test('trackpad: zoomed in, the viewport follows the cursor', () {
    final g = _gestures()..gestures.trackpad = true;
    g.viewport
      ..zoom = 2
      ..panX = 0.25
      ..panY = 0.25;
    g.down(1, 0, 100, Duration.zero);
    g.move(1, 180, 100, const Duration(seconds: 2));
    expect(g.gestures.cursor.x, closeTo(0.725, 1e-9));
    expect(g.gestures.cursorView.dx, closeTo(400 - deskEdgePx, 1e-9));
    expect(g.viewport.panY, 0.25);
  });

  test(
    'trackpad: tap-then-slide drags and two-finger tap act at the cursor',
    () {
      final g = _gestures()..gestures.trackpad = true;
      _tap(g, 50, 50, 0);
      g.events.clear();
      g.down(1, 50, 50, const Duration(milliseconds: 200));
      g.move(1, 70, 50, const Duration(milliseconds: 600));
      g.up(1, 70, 50, const Duration(milliseconds: 700));
      final drag = [for (final e in g.events) e as DeskDragEvent];
      expect(drag.map((e) => e.phase), [
        DeskDragPhase.down,
        DeskDragPhase.move,
        DeskDragPhase.up,
      ]);
      expect(drag[0].at.x, closeTo(0.5, 1e-9));
      expect(drag[2].at.x, closeTo(0.55, 1e-9));

      g.events.clear();
      g.down(1, 10, 10, const Duration(seconds: 1));
      g.down(2, 300, 190, const Duration(seconds: 1));
      g.up(2, 300, 190, const Duration(milliseconds: 1040));
      g.up(1, 10, 10, const Duration(milliseconds: 1050));
      final click = g.events.single as DeskRightClickEvent;
      expect(click.at.x, closeTo(0.55, 1e-9));
      expect(click.at.y, closeTo(0.5, 1e-9));
    },
  );

  test('trackpad: a tap first brings an off-screen cursor into view', () {
    final g = _gestures()..gestures.trackpad = true;
    g.viewport.zoom = 2;
    g.gestures.cursor = const DeskPoint(0.9, 0.5);
    _tap(g, 100, 100, 0);
    expect(g.gestures.cursorView.dx, closeTo(400 - deskEdgePx, 1e-9));
    final tap = g.events.single as DeskTapEvent;
    expect(tap.at.x, closeTo(0.9, 1e-9));
  });
}

class _Harness {
  _Harness._(this.gestures);
  final DeskGestures gestures;
  final events = <DeskEvent>[];

  DeskViewport get viewport => gestures.viewport;
  DeskFrame get fit => gestures.fit;
  bool get needsTick => gestures.needsTick;

  void down(int id, double x, double y, Duration time) =>
      gestures.down(id, x, y, time);
  void move(int id, double x, double y, Duration time) =>
      gestures.move(id, x, y, time);
  void up(int id, double x, double y, Duration time) =>
      gestures.up(id, x, y, time);
  void advance(Duration time) => gestures.advance(time);
}

_Harness _gestures({double width = 400, double height = 200}) {
  late _Harness harness;
  harness = _Harness._(
    DeskGestures(
      width: width,
      height: height,
      aspect: 2,
      onEvent: (event) => harness.events.add(event),
    ),
  );
  return harness;
}

void _tap(_Harness g, double x, double y, int ms) {
  final time = Duration(milliseconds: ms);
  g.down(1, x, y, time);
  g.up(1, x, y, time + const Duration(milliseconds: 20));
}
