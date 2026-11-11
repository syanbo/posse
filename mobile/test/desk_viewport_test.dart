import 'dart:convert';
import 'dart:ui';

import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/desk/chrome.dart';
import 'package:posse_mobile/desk/events.dart';
import 'package:posse_mobile/desk/keys.dart';
import 'package:posse_mobile/desk/plugin.dart';
import 'package:posse_mobile/desk/viewport.dart';
import 'package:posse_mobile/l10n/l10n.dart';

final zh = lookupAppLocalizations(const Locale('zh'));

void main() {
  test('contain fit letterboxes the long axis', () {
    final wide = fitContain(400, 200, 2);
    expect(wide.left, 0);
    expect(wide.top, 0);
    expect(wide.width, 400);
    expect(wide.height, 200);

    final tall = fitContain(200, 400, 2);
    expect(tall.left, 0);
    expect(tall.top, 150);
    expect(tall.width, 200);
    expect(tall.height, 100);
  });

  test('mapping uses the whole desktop, including after zoom and pan', () {
    const aspect = 2.0;
    final wide = fitContain(400, 200, aspect);
    final zoomed = DeskViewport(zoom: 2, panX: 0.25, panY: 0.25);
    final center = mapDesktop(x: 200, y: 100, fit: wide, viewport: zoomed);
    expect(center!.x, closeTo(0.5, 1e-9));
    expect(center.y, closeTo(0.5, 1e-9));

    final tall = fitContain(200, 400, aspect);
    final identity = DeskViewport();
    expect(mapDesktop(x: 100, y: 20, fit: tall, viewport: identity), isNull);
    final origin = mapDesktop(x: 0, y: 150, fit: tall, viewport: identity)!;
    expect(origin.x, closeTo(0, 1e-9));
    expect(origin.y, closeTo(0, 1e-9));
    final end = mapDesktop(x: 200, y: 250, fit: tall, viewport: identity)!;
    expect(end.x, closeTo(1, 1e-9));
    expect(end.y, closeTo(1, 1e-9));

    final shifted = DeskViewport(zoom: 2, panX: 0.25, panY: 0);
    final onPicture = mapDesktop(x: 0, y: 150, fit: tall, viewport: shifted)!;
    expect(onPicture.x, closeTo(0.25, 1e-9));
    expect(onPicture.y, closeTo(0, 1e-9));
    expect(mapDesktop(x: 100, y: 10, fit: tall, viewport: shifted), isNull);
  });

  test('viewport clamp stays inside 1 to 4', () {
    final view = DeskViewport(zoom: 9, panX: 3, panY: -1);
    view.clamp();
    expect(view.zoom, deskMaxZoom);
    expect(view.panX, closeTo(1 - 1 / deskMaxZoom, 1e-9));
    expect(view.panY, 0);

    view.zoom = 0.2;
    view.panX = 0.4;
    view.clamp();
    expect(view.zoom, deskMinZoom);
    expect(view.panX, 0);
    expect(view.panY, 0);
  });

  test('scaled image rect follows pan', () {
    final fit = fitContain(400, 200, 2);
    final rect = deskImageRect(
      fit,
      DeskViewport(zoom: 2, panX: 0.25, panY: 0.25),
    );
    expect(rect, const Rect.fromLTWH(-200, -100, 800, 400));
  });

  test('test key bytes match deskrecv, and port parsing rejects the ends', () {
    expect(utf8.encode(deskTestSecret).length, 31);
    expect(deskTestSecretBytes(), utf8.encode(deskTestSecret));
    expect(deskTestNonceBytes().length, 16);
    expect(parseDeskPort('47991'), deskDefaultPort);
    expect(parseDeskPort(' 80 '), 80);
    expect(parseDeskPort('0'), isNull);
    expect(parseDeskPort('65536'), isNull);
    expect(parseDeskPort(''), isNull);
  });

  test('host comes from the websocket url without its port', () {
    expect(deskHostOf('ws://192.168.1.8:6790/posse'), '192.168.1.8');
    expect(deskHostOf('wss://mac.local/posse'), 'mac.local');
    expect(deskHostOf(null), isNull);
    expect(deskHostOf('not a url'), isNull);
  });

  test('stats parse and bitrate labels', () {
    final stats = DeskStats.parse({
      'fps': 59.5,
      'bitrate': 2.5e6,
      'loss': 3,
      'decodeMs': 4.25,
      'recoveries': 2,
      'width': 1920,
      'height': 1080,
      'video': true,
      'public': '203.0.113.5:4100',
      'path': 'punched',
    });
    expect(stats.fps, 59.5);
    expect(stats.public, '203.0.113.5:4100');
    expect(deskPathLabel(zh, stats.path), '打洞直连');
    expect(deskPathLabel(zh, DeskStats.parse(null).path), '—');
    expect(stats.loss, 3);
    expect(stats.recoveries, 2);
    expect(stats.video, isTrue);
    expect(stats.width, 1920);
    expect(DeskStats.parse(null).video, isFalse);
    expect(deskBitrate(2.5e6), '2.5 Mbps');
    expect(deskBitrate(2000), '2 kbps');
    expect(deskBitrate(400), '400 bps');
    expect(deskByteRate(2.5e6), '313 KB/s');
    expect(deskByteRate(16e6), '2.0 MB/s');
  });

  test('event log keeps the latest drag move and six lines', () {
    final log = DeskEventLog();
    log.add(const DeskDragEvent(DeskDragPhase.down, DeskPoint(0.1, 0.2)));
    log.add(const DeskDragEvent(DeskDragPhase.move, DeskPoint(0.2, 0.2)));
    log.add(const DeskDragEvent(DeskDragPhase.move, DeskPoint(0.3, 0.2)));
    expect(log.events, hasLength(2));
    expect(deskEventLine(zh, log.events.last), '拖动 0.300, 0.200');
    for (var i = 0; i < 8; i++) {
      log.add(DeskTapEvent(DeskPoint(i / 10, 0.5), 1));
    }
    expect(log.events, hasLength(DeskEventLog.keep));
    expect(
      deskEventLine(zh, const DeskTapEvent(DeskPoint(0.25, 0.5), 2)),
      '2 击 0.250, 0.500',
    );
    expect(
      deskEventLine(zh, const DeskRightClickEvent(DeskPoint(0.3, 0.55))),
      '右键 0.300, 0.550',
    );
    expect(
      deskEventLine(zh, const DeskScrollEvent(12, -4, inertia: true)),
      '滚动 12, -4 惯性',
    );
    expect(deskEventLine(zh, const DeskKeyEvent('⌘C')), '⌘C');
    expect(deskEventLine(zh, const DeskTextEvent('你好')), '文字 你好');
  });

  test('rotation keeps the desktop point under the view center', () {
    final view = DeskViewport(zoom: 2, panX: 0.25, panY: 0.25);
    final fromFit = fitContain(400, 200, 2);
    final before = mapDesktop(x: 200, y: 100, fit: fromFit, viewport: view)!;
    expect(before.x, closeTo(0.5, 1e-9));
    expect(before.y, closeTo(0.5, 1e-9));

    final toFit = fitContain(200, 400, 2);
    keepDesktopCenter(
      viewport: view,
      from: fromFit,
      to: toFit,
      fromView: const Size(400, 200),
      toView: const Size(200, 400),
    );
    final after = mapDesktop(x: 100, y: 200, fit: toFit, viewport: view)!;
    expect(after.x, closeTo(before.x, 1e-9));
    expect(after.y, closeTo(before.y, 1e-9));
  });

  test('a frame that is not centered moves pan to hold the center', () {
    final view = DeskViewport(zoom: 2, panX: 0.4, panY: 0.2);
    const from = DeskFrame(0, 0, 400, 200);
    const to = DeskFrame(0, 0, 200, 200);
    keepDesktopCenter(
      viewport: view,
      from: from,
      to: to,
      fromView: const Size(400, 200),
      toView: const Size(400, 200),
    );
    // 原来中心在桌面 0.65, 0.45。新画面只有一半宽，pan 要退回来才还落在同一点。
    expect(view.panX, closeTo(0.15, 1e-9));
    expect(view.panY, closeTo(0.2, 1e-9));
    final after = mapDesktop(x: 200, y: 100, fit: to, viewport: view)!;
    expect(after.x, closeTo(0.65, 1e-9));
    expect(after.y, closeTo(0.45, 1e-9));
  });
}
