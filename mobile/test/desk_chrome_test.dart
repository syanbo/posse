import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/desk/chrome.dart';
import 'package:posse_mobile/desk/desk_page.dart';
import 'package:posse_mobile/desk/events.dart';
import 'package:posse_mobile/desk/gestures.dart';
import 'package:posse_mobile/desk/hint.dart';
import 'package:posse_mobile/desk/key_bar.dart';
import 'package:posse_mobile/desk/mods.dart';
import 'package:posse_mobile/desk/plugin.dart';
import 'package:posse_mobile/desk/toolbar.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/ui/theme.dart';

import 'shot_fonts.dart';

final zh = lookupAppLocalizations(const Locale('zh'));

void main() {
  test('landscape choice and the one-time hint round-trip', () async {
    expect(AppSettings().deskLandscape, isFalse);
    expect(AppSettings().deskGesturesSeen, isFalse);
    expect(
      AppSettings.decode(
        jsonEncode({'deskLandscape': 'yes', 'deskGesturesSeen': 1}),
      ).deskLandscape,
      isFalse,
    );
    final store = MemorySettingsStore();
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: store,
    );
    model.updateSettings((s) {
      s.deskLandscape = true;
      s.deskGesturesSeen = true;
      s.deskTrackpad = true;
    });
    await Future<void>.delayed(Duration.zero);
    final back = AppSettings.decode(store.data);
    expect(back.deskLandscape, isTrue);
    expect(back.deskGesturesSeen, isTrue);
    expect(back.deskTrackpad, isTrue);
    expect(AppSettings().deskTrackpad, isTrue);
    expect(deskLockedOrientations(false), [DeviceOrientation.portraitUp]);
    expect(deskLockedOrientations(true), [
      DeviceOrientation.landscapeLeft,
      DeviceOrientation.landscapeRight,
    ]);
    expect(deskAppOrientations, isEmpty);
  });

  test('floating control positions round-trip separately and invalid values fall back', () {
    final settings = AppSettings(
      deskToolbarPortrait: const Offset(0.3, 0.7),
      deskToolbarLandscape: const Offset(0.8, 0.4),
      deskBackPortrait: const Offset(0.1, 0.2),
      deskBackLandscape: const Offset(0.2, 0.5),
      deskStatusPortrait: const Offset(0.9, 0.1),
      deskStatusLandscape: const Offset(0.7, 0.8),
    );
    final back = AppSettings.decode(jsonEncode(settings.toJson()));
    expect(back.deskToolbarPortrait, const Offset(0.3, 0.7));
    expect(back.deskToolbarLandscape, const Offset(0.8, 0.4));
    expect(back.deskBackPortrait, const Offset(0.1, 0.2));
    expect(back.deskBackLandscape, const Offset(0.2, 0.5));
    expect(back.deskStatusPortrait, const Offset(0.9, 0.1));
    expect(back.deskStatusLandscape, const Offset(0.7, 0.8));
    final invalid = AppSettings.decode(
      jsonEncode({
        'deskToolbarPortrait': [-2, 3],
        'deskToolbarLandscape': ['bad', 0],
        'deskBackPortrait': [-1, 2],
        'deskStatusPortrait': ['bad', 0],
      }),
    );
    expect(invalid.deskToolbarPortrait, const Offset(0, 1));
    expect(invalid.deskToolbarLandscape, isNull);
    expect(invalid.deskBackPortrait, const Offset(0, 1));
    expect(invalid.deskStatusPortrait, isNull);
    final empty = AppSettings.decode('{}');
    expect(empty.deskToolbarPortrait, isNull);
    expect(empty.deskBackPortrait, isNull);
    expect(empty.deskStatusPortrait, isNull);
  });

  test('the shortcut bar stays up for an explicit open or the keyboard', () {
    final chrome = DeskChrome();
    expect(chrome.keysVisible(false), isFalse);
    expect(chrome.keysVisible(true), isTrue);
    chrome.toggleShortcuts();
    expect(chrome.keysVisible(false), isTrue);
    chrome.toggleShortcuts();
    expect(chrome.keysVisible(false), isFalse);
    expect(chrome.keysVisible(true), isTrue);
  });

  test('picture padding, fps and the right-click haptic', () {
    expect(
      deskPicturePadding(
        landscape: false,
        viewPadding: const EdgeInsets.only(left: 50, top: 59),
      ),
      EdgeInsets.zero,
    );
    expect(
      deskPicturePadding(
        landscape: true,
        viewPadding: const EdgeInsets.only(left: 59, right: 21),
      ),
      const EdgeInsets.only(left: 59, right: 21),
    );
    expect(deskBarTrack(390, 280), 110);
    expect(deskFpsLabel(zh, 59.6, video: true), '60 fps');
    expect(deskFpsLabel(zh, 0, video: false), '— fps');
    expect(deskFpsLabel(zh, -1, video: true), '— fps');
    expect(deskFpsLabel(zh, 0, video: true, still: true), '静止');
    expect(deskFpsValue(zh, 59.6, video: true), '60');
    expect(deskFpsValue(zh, 0, video: false), '—');
    expect(deskFpsValue(zh, -1, video: true), '—');
    expect(deskFpsValue(zh, 0, video: true, still: true), '静止');
    expect(
      deskHapticFor(const DeskRightClickEvent(DeskPoint(0.2, 0.3))),
      isTrue,
    );
    expect(
      deskHapticFor(const DeskDragEvent(DeskDragPhase.down, DeskPoint(0, 0))),
      isFalse,
    );
  });

  test('a long press arms the drag once', () {
    var arms = 0;
    final events = <DeskEvent>[];
    final g = DeskGestures(
      width: 400,
      height: 200,
      aspect: 2,
      onEvent: events.add,
      onArm: () => arms++,
    );
    g.down(1, 100, 80, Duration.zero);
    g.advance(const Duration(milliseconds: 299));
    expect(arms, 0);
    g.advance(const Duration(milliseconds: 300));
    expect(arms, 1);
    g.move(1, 130, 80, const Duration(milliseconds: 360));
    expect(arms, 1);
    expect(g.dragging, isTrue);
    expect(events.first, isA<DeskDragEvent>());
    expect((events.first as DeskDragEvent).phase, DeskDragPhase.down);
  });

  test('a move past the slop after the hold arms inside the move', () {
    var arms = 0;
    final events = <DeskEvent>[];
    final g = DeskGestures(
      width: 400,
      height: 200,
      aspect: 2,
      onEvent: events.add,
      onArm: () => arms++,
    );
    g.down(1, 100, 80, Duration.zero);
    g.move(1, 130, 80, const Duration(milliseconds: 299));
    expect(arms, 0);
    expect(g.dragging, isFalse);
    g.up(1, 130, 80, const Duration(milliseconds: 299));

    g.down(1, 100, 80, const Duration(milliseconds: 299));
    g.move(1, 130, 80, const Duration(milliseconds: 599));
    expect(arms, 1);
    expect(g.dragging, isTrue);
    expect(events.first, isA<DeskDragEvent>());
    expect((events.first as DeskDragEvent).phase, DeskDragPhase.down);
  });

  testWidgets('the hint goes away on any tap and does not come back', (
    tester,
  ) async {
    var seen = false;
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: StatefulBuilder(
          builder: (context, setState) {
            if (seen) return const Text('gone');
            return DeskGestureHint(
              onDismiss: () => setState(() => seen = true),
            );
          },
        ),
      ),
    );
    expect(find.text('直接点按模式'), findsOneWidget);
    expect(find.text('右键'), findsOneWidget);
    expect(find.text('双指捏合'), findsOneWidget);
    expect(find.text('长按到震一下，再拖'), findsOneWidget);
    await tester.tap(find.byKey(const Key('desk-gesture-hint')));
    await tester.pump();
    expect(find.text('gone'), findsOneWidget);
    expect(find.text('右键'), findsNothing);
  });

  testWidgets('connection status shows compact fps and expands only on alert', (
    tester,
  ) async {
    Future<void> pump(
      int? latency, {
      bool unresponsive = false,
      bool reconnecting = false,
      double fps = 59.6,
      bool video = true,
      bool still = false,
    }) => tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: Scaffold(
          body: DeskStatusPill(
            latency: latency == null ? null : Duration(milliseconds: latency),
            unresponsive: unresponsive,
            reconnecting: reconnecting,
            detailsOpen: false,
            fps: fps,
            video: video,
            still: still,
            onTap: () {},
          ),
        ),
      ),
    );
    final status = find.byKey(const Key('desk-status'));
    await pump(299);
    expect(tester.getSize(status), const Size.square(deskControlExtent));
    expect(find.text('60'), findsOneWidget);
    expect(find.text(zh.deskHighLatency), findsNothing);
    await pump(299, fps: 0, still: true);
    expect(tester.getSize(status), const Size.square(deskControlExtent));
    expect(find.text(zh.deskFpsStill), findsOneWidget);
    await pump(299, fps: 0, video: false);
    expect(tester.getSize(status), const Size.square(deskControlExtent));
    expect(find.text('—'), findsOneWidget);
    await pump(300);
    expect(find.text('60'), findsOneWidget);
    expect(find.text(zh.deskHighLatency), findsOneWidget);
    await pump(300, unresponsive: true);
    expect(find.text(zh.chatLatencyUnresponsive), findsOneWidget);
    await pump(null, reconnecting: true);
    expect(find.text(zh.deskReconnecting), findsOneWidget);
  });

  testWidgets('the toolbar starts collapsed and collapses after key controls', (
    tester,
  ) async {
    addTearDown(tester.view.reset);
    _surface(tester, const Size(800, 400), FakeViewPadding.zero);
    _mockDeskChannels(tester);
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    addTearDown(model.dispose);
    model.updateSettings((s) => s.deskGesturesSeen = true);
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: DeskPage(model: model),
      ),
    );
    await tester.pumpAndSettle();
    final bar = find.byKey(const Key('desk-toolbar'));
    final toggle = find.byKey(const Key('desk-toolbar-toggle'));
    final shortcuts = find.byKey(const Key('desk-toolbar-shortcuts'));
    final keyboard = find.byKey(const Key('desk-toolbar-keyboard'));
    final settings = find.byKey(const Key('desk-toolbar-settings'));
    final home = tester.getCenter(toggle);
    expect(tester.getSize(bar), const Size.square(deskControlExtent));
    expect(shortcuts, findsNothing);

    await _expandToolbar(tester);
    expect(tester.getCenter(toggle), home);
    expect(
      tester.getCenter(keyboard).dx,
      lessThan(tester.getCenter(shortcuts).dx),
    );
    expect(
      tester.getCenter(shortcuts).dx,
      lessThan(tester.getCenter(settings).dx),
    );
    expect(find.byIcon(Icons.link_off_rounded), findsNothing);
    expect(find.byIcon(Icons.speed_rounded), findsNothing);
    await tester.tap(toggle);
    await tester.pumpAndSettle();
    expect(shortcuts, findsNothing);

    await _expandToolbar(tester);
    await tester.tap(shortcuts);
    await tester.pumpAndSettle();
    expect(find.byType(DeskKeyBar), findsOneWidget);
    expect(shortcuts, findsNothing);

    Finder keyBarKeyboard(IconData icon) => find.descendant(
      of: find.byType(DeskKeyBar),
      matching: find.byIcon(icon),
    );
    await _expandToolbar(tester);
    await tester.tap(keyBarKeyboard(Icons.keyboard_rounded));
    await tester.pumpAndSettle();
    expect(keyboard, findsNothing);
    await tester.tap(keyBarKeyboard(Icons.keyboard_hide_rounded));
    await tester.pumpAndSettle();

    await _expandToolbar(tester);
    await tester.tap(shortcuts);
    await tester.pumpAndSettle();
    expect(find.byType(DeskKeyBar), findsNothing);

    await _expandToolbar(tester);
    await tester.tap(keyboard);
    await tester.pumpAndSettle();
    expect(find.byType(DeskKeyBar), findsOneWidget);
    expect(keyboard, findsNothing);
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pumpAndSettle();
  });

  testWidgets('taps beside the toolbar still reach the picture', (
    tester,
  ) async {
    var stage = 0;
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: Stack(
            children: [
              Positioned.fill(
                child: GestureDetector(
                  onTap: () => stage++,
                  child: const ColoredBox(color: Color(0xFF000000)),
                ),
              ),
              Positioned(
                top: 20,
                left: 0,
                right: 0,
                bottom: 0,
                child: DeskBarSlot(
                  anchor: ValueNotifier<Offset?>(const Offset(0.5, 0)),
                  onDragEnd: () {},
                  child: const SizedBox(
                    key: Key('desk-pill'),
                    width: 80,
                    height: 28,
                    child: ColoredBox(color: Color(0xFFFFFFFF)),
                  ),
                ),
              ),
            ],
          ),
        ),
      ),
    );
    await tester.pump();
    await tester.tapAt(const Offset(20, 34));
    expect(stage, 1);
    final pill = tester.getRect(find.byKey(const Key('desk-pill')));
    await tester.tapAt(pill.center);
    expect(stage, 1);
  });

  testWidgets('a landscape key bar is the short one', (tester) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: Scaffold(
          body: DeskKeyBar(
            mods: DeskMods(),
            keyboardUp: false,
            dense: true,
            onKeyboard: () {},
            onKey: (_) {},
            onModTap: (_) {},
            onModLock: (_) {},
          ),
        ),
      ),
    );
    expect(
      find.byWidgetPredicate(
        (widget) => widget is SizedBox && widget.height == deskKeyBarDense,
      ),
      findsOneWidget,
    );
    final box = tester.widget<Container>(find.byType(Container).first);
    final color = (box.decoration! as BoxDecoration).color!;
    expect(color.a, lessThan(1));
  });

  test('the desk route turns off the back swipe', () {
    expect(
      DeskRoute(builder: (_) => const SizedBox()).popGestureEnabled,
      isFalse,
    );
  });

  testWidgets('the orientation button shows the current orientation', (
    tester,
  ) async {
    var turns = 0;
    Future<void> pump(bool landscape) {
      return tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          home: Scaffold(
            body: Center(
              child: DeskToolbar(
                landscape: landscape,
                expanded: true,
                keyboardUp: false,
                shortcuts: false,
                onToggle: () {},
                onOrientation: () => turns++,
                onKeyboard: () {},
                onShortcuts: () {},
                onSettings: () {},
              ),
            ),
          ),
        ),
      );
    }

    IconData icon() => tester
        .widget<Icon>(
          find.descendant(
            of: find.byKey(const Key('desk-toolbar-orientation')),
            matching: find.byType(Icon),
          ),
        )
        .icon!;

    await pump(false);
    await tester.pump(const Duration(milliseconds: 200));
    expect(icon(), Icons.stay_current_portrait_rounded);
    await tester.tap(find.byKey(const Key('desk-toolbar-orientation')));
    expect(turns, 1);
    await pump(true);
    await tester.pump(const Duration(milliseconds: 200));
    expect(icon(), Icons.stay_current_landscape_rounded);
  });

  testWidgets('a drag after the bar finishes opening uses the opened width', (
    tester,
  ) async {
    final anchor = ValueNotifier<Offset?>(const Offset(0.5, 0));
    addTearDown(anchor.dispose);
    var expanded = false;
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: SizedBox(
            width: 300,
            height: 120,
            child: StatefulBuilder(
              builder: (context, setState) {
                return Column(
                  children: [
                    Expanded(
                      child: DeskBarSlot(
                        anchor: anchor,
                        onDragEnd: () {},
                        child: AnimatedSize(
                          duration: const Duration(milliseconds: 160),
                          child: ColoredBox(
                            key: const Key('desk-pill'),
                            color: const Color(0xFFFFFFFF),
                            child: SizedBox(
                              width: expanded ? 200 : 40,
                              height: 28,
                            ),
                          ),
                        ),
                      ),
                    ),
                    TextButton(
                      onPressed: () => setState(() => expanded = true),
                      child: const Text('open'),
                    ),
                  ],
                );
              },
            ),
          ),
        ),
      ),
    );
    await tester.pump();
    await tester.tap(find.text('open'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 200));
    final pill = tester.getCenter(find.byKey(const Key('desk-pill')));
    final finger = await tester.startGesture(pill);
    await tester.pump();
    // 先过拖动的起步距离，再量这一段移动
    await finger.moveBy(const Offset(30, 0));
    await tester.pump();
    final start = anchor.value!.dx;
    await finger.moveBy(const Offset(24, 0));
    await tester.pump();
    expect(anchor.value!.dx - start, closeTo(24 / (300 - 200), 0.05));
    final beforeBatch = anchor.value!.dx;
    await finger.moveBy(const Offset(-5, 0));
    await finger.moveBy(const Offset(-5, 0));
    await tester.pump();
    expect(anchor.value!.dx - beforeBatch, closeTo(-10 / (300 - 200), 0.001));
    final released = anchor.value;
    await finger.up();
    await tester.pump();
    expect(anchor.value, released);
  });

  testWidgets(
    'entering locks orientation and edges, a quick return is not undone',
    (tester) async {
      final orientations = <List<String>>[];
      final edges = <bool>[];
      _mockDeskChannels(tester, orientations: orientations, edges: edges);
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
      );
      addTearDown(model.dispose);
      model.updateSettings((s) => s.deskGesturesSeen = true);

      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          home: const SizedBox.shrink(),
        ),
      );
      final nav = tester.state<NavigatorState>(find.byType(Navigator));
      nav.push(DeskRoute(builder: (_) => DeskPage(model: model)));
      await tester.pumpAndSettle();
      expect(orientations, isNotEmpty);
      expect(orientations.last, ['DeviceOrientation.portraitUp']);
      expect(edges, [true]);

      nav.pop();
      nav.push(DeskRoute(builder: (_) => DeskPage(model: model)));
      await tester.pumpAndSettle();
      expect(orientations.last, ['DeviceOrientation.portraitUp']);
      expect(edges, isNot(contains(false)));

      await tester.tap(find.byKey(const Key('desk-toolbar-back')));
      await tester.pumpAndSettle();
      expect(find.byType(DeskPage), findsNothing);
      expect(orientations.last, isEmpty);
      expect(edges.last, isFalse);
    },
  );

  testWidgets('a reopened desk waits for the previous desktop stream to end', (
    tester,
  ) async {
    _mockDeskChannels(tester);
    final rpc = _DeskRpc()..sendEndOnUnsubscribe = false;
    final model = _DeskModel(rpc);
    final receiver = _DeskReceiver();
    addTearDown(model.dispose);
    addTearDown(rpc.frames.close);
    addTearDown(receiver.frames.close);
    model.updateSettings((s) => s.deskGesturesSeen = true);
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: const SizedBox.shrink(),
      ),
    );
    final nav = tester.state<NavigatorState>(find.byType(Navigator));
    DeskRoute route() => DeskRoute(
      builder: (_) => DeskPage(model: model, receiver: receiver),
    );
    Future<void> transition() async {
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 600));
      await _flushStreamCleanup(tester);
    }

    nav.push(route());
    await transition();
    expect(rpc.calls, ['desk.subscribe']);

    nav.pop();
    nav.push(route());
    await transition();
    expect(rpc.calls, ['desk.subscribe', 'desk.unsubscribe']);
    await tester.tapAt(tester.getCenter(find.byType(DeskPage)));
    await tester.pump();
    expect(receiver.inputs, 0);

    rpc.frames.add({'type': 'end', 'reason': 'unsubscribed'});
    await _flushStreamCleanup(tester);
    expect(rpc.calls, ['desk.subscribe', 'desk.unsubscribe', 'desk.subscribe']);

    rpc.sendEndOnUnsubscribe = true;
    await tester.tap(find.byKey(const Key('desk-toolbar-back')));
    await transition();
    expect(rpc.calls.last, 'desk.unsubscribe');
  });

  testWidgets(
    'free position persists and header stays fixed across keyboard and rotation',
    (tester) async {
      addTearDown(tester.view.reset);
      _mockDeskChannels(tester);
      final store = MemorySettingsStore();
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: store,
      );
      addTearDown(model.dispose);
      model.updateSettings((s) => s.deskGesturesSeen = true);
      _surface(
        tester,
        const Size(400, 800),
        const FakeViewPadding(top: 47, bottom: 34),
      );
      Future<void> open() => tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          home: DeskPage(model: model),
        ),
      );
      await open();
      await tester.pumpAndSettle();
      Rect bar() => tester.getRect(find.byKey(const Key('desk-toolbar')));
      Rect back() => tester.getRect(find.byKey(const Key('desk-toolbar-back')));
      Rect status() => tester.getRect(find.byKey(const Key('desk-status')));
      final toggle = find.byKey(const Key('desk-toolbar-toggle'));
      Future<void> checkTogglePosition() async {
        final before = tester.getCenter(toggle);
        await _expandToolbar(tester);
        expect((tester.getCenter(toggle) - before).distance, lessThan(0.001));
        await tester.tap(toggle);
        await tester.pumpAndSettle();
        expect((tester.getCenter(toggle) - before).distance, lessThan(0.001));
      }

      final first = bar();
      final backAt = back();
      final statusAt = status();
      expect(first.right, 392);
      expect(first.top, greaterThan(backAt.bottom));
      expect(first.size, const Size.square(deskControlExtent));
      await tester.drag(
        find.byKey(const Key('desk-toolbar-toggle')),
        const Offset(-220, 300),
      );
      await tester.pumpAndSettle();
      final moved = bar();
      expect(moved.left, greaterThan(50));
      expect(moved.right, lessThan(300));
      expect(moved.top, greaterThan(first.top + 200));
      expect(find.byKey(const Key('desk-toolbar-settings')), findsNothing);
      expect(back(), backAt);
      expect(status(), statusAt);
      final saved = model.settings.deskToolbarPortrait;
      expect(AppSettings.decode(store.data).deskToolbarPortrait, saved);
      await checkTogglePosition();

      // 键盘只推开实际被遮住的工具条，不改变偏好；固定控件也不跟着动。
      tester.view.viewInsets = const FakeViewPadding(bottom: 450);
      await tester.pumpAndSettle();
      expect(bar().bottom, lessThanOrEqualTo(342));
      expect(bar().size, first.size);
      expect(model.settings.deskToolbarPortrait, saved);
      expect(back(), backAt);
      expect(status(), statusAt);
      tester.view.viewInsets = FakeViewPadding.zero;
      await tester.pumpAndSettle();
      expect(bar(), moved);

      _surface(
        tester,
        const Size(800, 400),
        const FakeViewPadding(left: 59, bottom: 21),
      );
      await tester.pumpAndSettle();
      expect(bar().right, 792);
      expect(bar().size, first.size);
      await tester.drag(
        find.byKey(const Key('desk-toolbar-toggle')),
        const Offset(-180, 100),
      );
      await tester.pumpAndSettle();
      final side = bar();
      expect(model.settings.deskToolbarLandscape, isNotNull);
      await checkTogglePosition();

      _surface(
        tester,
        const Size(400, 800),
        const FakeViewPadding(top: 47, bottom: 34),
      );
      await tester.pumpAndSettle();
      expect(bar(), moved);
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pumpAndSettle();
      await open();
      await tester.pumpAndSettle();
      expect(bar(), moved);
      _surface(
        tester,
        const Size(800, 400),
        const FakeViewPadding(left: 59, bottom: 21),
      );
      await tester.pumpAndSettle();
      expect(bar(), side);
      await _expandToolbar(tester);
      await tester.drag(toggle, const Offset(-70, 45));
      await tester.pumpAndSettle();
      final expandedToggle = tester.getCenter(toggle);
      await tester.tap(toggle);
      await tester.pumpAndSettle();
      expect(
        (tester.getCenter(toggle) - expandedToggle).distance,
        lessThan(0.001),
      );
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pumpAndSettle();
    },
  );

  for (final hold in [
    Duration.zero,
    const Duration(milliseconds: 180),
    const Duration(milliseconds: 600),
  ]) {
    testWidgets(
      'dragging a toolbar button after ${hold.inMilliseconds}ms never taps it or the desktop',
      (tester) async {
        final anchor = ValueNotifier<Offset?>(const Offset(0.7, 0.2));
        addTearDown(anchor.dispose);
        var help = 0;
        var desktop = 0;
        var saves = 0;
        await tester.pumpWidget(
          MaterialApp(
            theme: posseTheme(Brightness.dark),
            home: Scaffold(
              body: Stack(
                children: [
                  Positioned.fill(
                    child: GestureDetector(
                      onTap: () => desktop++,
                      onPanUpdate: (_) => desktop++,
                      child: const ColoredBox(color: Colors.black),
                    ),
                  ),
                  Positioned.fill(
                    child: DeskBarSlot(
                      anchor: anchor,
                      onDragEnd: () => saves++,
                      child: _toolbar(onSettings: () => help++),
                    ),
                  ),
                ],
              ),
            ),
          ),
        );
        final before = anchor.value;
        final gesture = await tester.startGesture(
          tester.getCenter(find.byKey(const Key('desk-toolbar-settings'))),
        );
        await tester.pump(hold);
        await gesture.moveBy(const Offset(-30, 18));
        await tester.pump(const Duration(milliseconds: 100));
        await gesture.moveBy(const Offset(-70, 42));
        await gesture.up();
        await tester.pumpAndSettle();
        expect(anchor.value, isNot(before));
        expect(help, 0);
        expect(desktop, 0);
        expect(saves, 1);
        await tester.tap(find.byKey(const Key('desk-toolbar-settings')));
        expect(help, 1);
        expect(saves, 1);
      },
    );
  }

  testWidgets('dragging a full-width toolbar preserves its horizontal anchor', (
    tester,
  ) async {
    final anchor = ValueNotifier<Offset?>(const Offset(1, 0));
    addTearDown(anchor.dispose);
    Future<void> pump(double width) => tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: Scaffold(
          body: SizedBox(
            width: width,
            height: 180,
            child: DeskBarSlot(
              anchor: anchor,
              onDragEnd: () {},
              child: _toolbar(landscape: true),
            ),
          ),
        ),
      ),
    );
    await pump(240);
    await tester.pumpAndSettle();
    await tester.drag(
      find.byKey(const Key('desk-toolbar-toggle')),
      const Offset(-30, 24),
    );
    await tester.pumpAndSettle();
    expect(anchor.value!.dx, 1);
    expect(anchor.value!.dy, greaterThan(0));
    await pump(500);
    await tester.pumpAndSettle();
    expect(tester.getRect(find.byKey(const Key('desk-toolbar'))).right, 500);
  });

  for (final (screen, keyboard) in [
    (const Size(568, 320), 250.0),
    (const Size(280, 568), 470.0),
  ]) {
    testWidgets('controls remain reachable above tall keyboard on $screen', (
      tester,
    ) async {
      addTearDown(tester.view.reset);
      _mockDeskChannels(tester);
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
      );
      addTearDown(model.dispose);
      model.updateSettings((s) => s.deskGesturesSeen = true);
      _surface(tester, screen, FakeViewPadding.zero);
      tester.view.viewInsets = FakeViewPadding(bottom: keyboard);
      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          home: DeskPage(model: model),
        ),
      );
      await tester.pumpAndSettle();
      await _expandToolbar(tester);
      Rect rect(String key) => tester.getRect(find.byKey(Key(key)));
      final bar = rect('desk-toolbar');
      expect(bar.height, deskControlExtent);
      expect(bar.bottom, lessThanOrEqualTo(screen.height - keyboard));
      expect(bar.overlaps(rect('desk-toolbar-back')), isFalse);
      expect(bar.overlaps(rect('desk-status')), isFalse);
      final scroll = find.descendant(
        of: find.byKey(const Key('desk-toolbar')),
        matching: find.byType(SingleChildScrollView),
      );
      if (scroll.evaluate().isNotEmpty) {
        await tester.drag(scroll, const Offset(-500, 0));
        await tester.pumpAndSettle();
      }
      final settings = rect('desk-toolbar-settings');
      expect(settings.left, greaterThanOrEqualTo(bar.left));
      expect(settings.right, lessThanOrEqualTo(bar.right));
      expect(tester.takeException(), isNull);
      tester.view.viewInsets = FakeViewPadding.zero;
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('desk-toolbar-toggle')), findsOneWidget);
      expect(model.settings.deskToolbarPortrait, isNull);
      expect(model.settings.deskToolbarLandscape, isNull);
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pumpAndSettle();
    });
  }

  testWidgets(
    'back, status, and toolbar drag into safe area and persist positions',
    (tester) async {
      addTearDown(tester.view.reset);
      _mockDeskChannels(tester);
      final store = MemorySettingsStore();
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: store,
      );
      addTearDown(model.dispose);
      model.updateSettings((s) => s.deskGesturesSeen = true);
      _surface(
        tester,
        const Size(400, 800),
        const FakeViewPadding(top: 47, bottom: 34, left: 24, right: 24),
      );
      Future<void> open() => tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          home: DeskPage(model: model),
        ),
      );
      await open();
      await tester.pumpAndSettle();

      Rect rect(String key) => tester.getRect(find.byKey(Key(key)));
      // 默认位置保持在安全区内，返回和 FPS 按钮大小一致
      expect(rect('desk-toolbar-back').topLeft, const Offset(32, 47));
      expect(rect('desk-status').topRight, const Offset(368, 47));
      expect(rect('desk-status').size, rect('desk-toolbar-back').size);
      expect(
        rect('desk-toolbar').topRight,
        const Offset(368, 47 + deskControlExtent + 8),
      );

      // 三个按钮都可以拖进安全区（顶部、左/右侧、底部）
      await tester.drag(
        find.byKey(const Key('desk-toolbar-back')),
        const Offset(-80, -80),
      );
      await tester.pumpAndSettle();
      final backMoved = rect('desk-toolbar-back');
      expect(backMoved.left, 0);
      expect(backMoved.top, 0);

      await tester.drag(
        find.byKey(const Key('desk-status')),
        const Offset(80, -80),
      );
      await tester.pumpAndSettle();
      final statusMoved = rect('desk-status');
      expect(statusMoved.right, 400);
      expect(statusMoved.top, 0);

      await tester.drag(
        find.byKey(const Key('desk-toolbar-toggle')),
        const Offset(40, 800),
      );
      await tester.pumpAndSettle();
      final barMoved = rect('desk-toolbar');
      expect(barMoved.right, 400);
      expect(barMoved.bottom, 800);

      // 位置持久化并在重新进入页面后恢复
      final decoded = AppSettings.decode(store.data);
      expect(decoded.deskBackPortrait, model.settings.deskBackPortrait);
      expect(decoded.deskStatusPortrait, model.settings.deskStatusPortrait);
      expect(decoded.deskToolbarPortrait, model.settings.deskToolbarPortrait);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pumpAndSettle();
      await open();
      await tester.pumpAndSettle();
      expect(rect('desk-toolbar-back'), backMoved);
      expect(rect('desk-status'), statusMoved);
      expect(rect('desk-toolbar'), barMoved);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pumpAndSettle();
    },
  );

  testWidgets(
    'legacy default toolbar [1,0] migrates below status and stays clickable',
    (tester) async {
      addTearDown(tester.view.reset);
      _mockDeskChannels(tester);
      final store = MemorySettingsStore()
        ..data = jsonEncode({
          'deskGesturesSeen': true,
          'deskToolbarPortrait': [1, 0],
          'deskToolbarLandscape': [0.4, 0.5],
        });
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: store,
      );
      addTearDown(model.dispose);
      await model.restore();
      expect(model.settings.deskToolbarPortrait, isNull);
      expect(model.settings.deskToolbarLandscape, const Offset(0.4, 0.5));

      _surface(tester, const Size(400, 800), FakeViewPadding.zero);
      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          home: DeskPage(model: model),
        ),
      );
      await tester.pumpAndSettle();
      final status = tester.getRect(find.byKey(const Key('desk-status')));
      final bar = tester.getRect(find.byKey(const Key('desk-toolbar')));
      expect(bar.overlaps(status), isFalse);
      expect(bar.top, greaterThanOrEqualTo(status.bottom));
      await tester.tap(find.byKey(const Key('desk-toolbar-toggle')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('desk-toolbar-settings')), findsOneWidget);
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pumpAndSettle();
    },
  );

  testWidgets(
    'controls dragged to the same corner do not overlap and stay clickable',
    (tester) async {
      addTearDown(tester.view.reset);
      _mockDeskChannels(tester);
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
      );
      addTearDown(model.dispose);
      model.updateSettings((s) => s.deskGesturesSeen = true);
      _surface(tester, const Size(400, 800), FakeViewPadding.zero);
      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          home: DeskPage(model: model),
        ),
      );
      await tester.pumpAndSettle();

      await tester.drag(
        find.byKey(const Key('desk-toolbar-toggle')),
        const Offset(400, 800),
      );
      await tester.pumpAndSettle();
      await tester.drag(
        find.byKey(const Key('desk-status')),
        const Offset(400, 800),
      );
      await tester.pumpAndSettle();

      final status = tester.getRect(find.byKey(const Key('desk-status')));
      final bar = tester.getRect(find.byKey(const Key('desk-toolbar')));
      expect(status.overlaps(bar), isFalse);

      await tester.tap(find.byKey(const Key('desk-toolbar-toggle')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('desk-toolbar-settings')), findsOneWidget);
      await tester.tap(find.byKey(const Key('desk-status')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('desk-debug-panel')), findsOneWidget);

      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pumpAndSettle();
    },
  );

  testWidgets('status pill handles 3x text scaling without overflow', (
    tester,
  ) async {
    for (final locale in const [Locale('zh'), Locale('en')]) {
      await tester.pumpWidget(
        MaterialApp(
          locale: locale,
          localizationsDelegates: AppLocalizations.localizationsDelegates,
          supportedLocales: AppLocalizations.supportedLocales,
          theme: posseTheme(Brightness.dark),
          builder: (context, child) => MediaQuery(
            data: MediaQuery.of(context)
                .copyWith(textScaler: const TextScaler.linear(3.0)),
            child: child!,
          ),
          home: Scaffold(
            body: DeskStatusPill(
              latency: const Duration(milliseconds: 20),
              unresponsive: false,
              reconnecting: false,
              detailsOpen: false,
              fps: 0,
              video: true,
              still: true,
              onTap: () {},
            ),
          ),
        ),
      );
      expect(tester.takeException(), isNull);
      expect(
        tester.getSize(find.byKey(const Key('desk-status'))),
        const Size.square(deskControlExtent),
      );
    }
  });

  testWidgets('rotation mid-drag still persists the moved position', (
    tester,
  ) async {
    addTearDown(tester.view.reset);
    _mockDeskChannels(tester);
    final store = MemorySettingsStore();
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: store,
    );
    addTearDown(model.dispose);
    model.updateSettings((s) => s.deskGesturesSeen = true);
    _surface(tester, const Size(400, 800), FakeViewPadding.zero);
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: DeskPage(model: model),
      ),
    );
    await tester.pumpAndSettle();

    final gesture = await tester.startGesture(
      tester.getCenter(find.byKey(const Key('desk-toolbar-back'))),
    );
    await tester.pump();
    // 先越过拖动起步距离，再移动一段，此时不松手直接旋转。
    await gesture.moveBy(const Offset(30, 30));
    await tester.pump();
    await gesture.moveBy(const Offset(60, 100));
    await tester.pump();
    // 未松手直接切横屏：应把刚才拖动的位置落盘，不与内存分离。
    _surface(tester, const Size(800, 400), FakeViewPadding.zero);
    await tester.pumpAndSettle();
    await gesture.up();
    await tester.pumpAndSettle();
    expect(model.settings.deskBackPortrait, isNotNull);
    expect(
      AppSettings.decode(store.data).deskBackPortrait,
      model.settings.deskBackPortrait,
    );
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pumpAndSettle();
  });

  testWidgets('connection details open from the signal and tuck away tools', (
    tester,
  ) async {
    addTearDown(tester.view.reset);
    _mockDeskChannels(tester);
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    addTearDown(model.dispose);
    model.updateSettings((s) => s.deskGesturesSeen = true);
    _surface(tester, const Size(700, 320), const FakeViewPadding(left: 44));
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: DeskPage(model: model),
      ),
    );
    await tester.pumpAndSettle();
    await _expandToolbar(tester);
    final signal = find.byKey(const Key('desk-status'));
    await tester.tap(signal);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('desk-toolbar-keyboard')), findsNothing);
    expect(find.text(zh.deskDebugLoss('0')), findsOneWidget);
    expect(find.text(zh.deskDebugHeadline('— fps', '0')), findsOneWidget);
    final bar = tester.getRect(find.byKey(const Key('desk-toolbar')));
    final details = find.byKey(const Key('desk-debug-panel'));
    final panel = tester.getRect(details);
    expect(panel.top, greaterThan(bar.bottom));
    expect(bar.overlaps(panel), isFalse);
    await tester.tap(signal);
    await tester.pumpAndSettle();
    expect(details, findsNothing);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pumpAndSettle();
  });

  testWidgets(
    'desktop settings update the active mode without leaving the page',
    (tester) async {
      addTearDown(tester.view.reset);
      _mockDeskChannels(tester);
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
      );
      addTearDown(model.dispose);
      model.updateSettings((s) => s.deskGesturesSeen = true);
      _surface(
        tester,
        const Size(390, 844),
        const FakeViewPadding(top: 47, bottom: 34),
      );
      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          home: DeskPage(model: model),
        ),
      );
      await tester.pumpAndSettle();
      await _expandToolbar(tester);
      await tester.tap(find.byKey(const Key('desk-toolbar-settings')));
      await tester.pumpAndSettle();
      expect(find.text(zh.settingsDeskPasswords), findsOneWidget);
      await tester.tap(find.text(zh.settingsDeskDirect));
      await tester.pumpAndSettle();
      expect(model.settings.deskTrackpad, isFalse);
      await tester.tapAt(const Offset(20, 70));
      await tester.pumpAndSettle();
      expect(find.byType(DeskPage), findsOneWidget);
      expect(find.byKey(const Key('desk-gesture-hint')), findsOneWidget);
      expect(find.text('直接点按模式'), findsOneWidget);
      await tester.tap(find.byKey(const Key('desk-gesture-hint')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('desk-toolbar-settings')), findsNothing);
      await _expandToolbar(tester);
      await tester.tap(find.byKey(const Key('desk-toolbar-settings')));
      await tester.pumpAndSettle();
      await tester.tap(find.text(zh.deskGestureTitle));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('desk-gesture-hint')), findsOneWidget);
      expect(find.text(zh.settingsDeskPasswords), findsNothing);
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pumpAndSettle();
    },
  );

  final shots = Platform.environment['POSSE_SHOTS_DIR'] ?? '';
  testWidgets('desktop controls screenshots', (tester) async {
    addTearDown(tester.view.reset);
    _mockDeskChannels(tester);
    await loadShotFonts(tester);
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    addTearDown(model.dispose);
    model.updateSettings((s) => s.deskGesturesSeen = true);
    final key = GlobalKey();
    Future<void> shot(String name) => tester.runAsync(() async {
      final boundary =
          key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
      final image = await boundary.toImage(pixelRatio: 2);
      final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
      File('$shots/$name.png').writeAsBytesSync(bytes!.buffer.asUint8List());
      image.dispose();
    });
    for (final dark in [true, false]) {
      for (final landscape in [false, true]) {
        _surface(
          tester,
          landscape ? const Size(844, 390) : const Size(390, 844),
          landscape
              ? const FakeViewPadding(left: 47, bottom: 21)
              : const FakeViewPadding(top: 47, bottom: 34),
        );
        await tester.pumpWidget(
          RepaintBoundary(
            key: key,
            child: MaterialApp(
              debugShowCheckedModeBanner: false,
              theme: shotTheme(
                posseTheme(dark ? Brightness.dark : Brightness.light),
              ),
              home: DeskPage(model: model),
            ),
          ),
        );
        await tester.pumpAndSettle();
        final suffix =
            '${landscape ? 'landscape' : 'portrait'}-${dark ? 'dark' : 'light'}';
        await shot('desk-controls-$suffix');
        await _expandToolbar(tester);
        await shot('desk-controls-expanded-$suffix');
        await tester.tap(find.byKey(const Key('desk-toolbar-settings')));
        await tester.pumpAndSettle();
        await shot('desk-settings-$suffix');
        tester.state<NavigatorState>(find.byType(Navigator)).pop();
        await tester.pumpAndSettle();
      }
    }
    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pumpAndSettle();
  }, skip: shots.isEmpty);

  test('the picture rises just enough to show the last tap', () {
    // 没点过不动
    expect(deskLift(targetY: null, screenHeight: 800, covered: 300), 0);
    // 点在露着的地方不动
    expect(deskLift(targetY: 200, screenHeight: 800, covered: 300), 0);
    // 点在会被盖住的地方：露到盖住的边上面 48
    expect(deskLift(targetY: 600, screenHeight: 800, covered: 300), 148);
    // 最多移盖住的那么高
    expect(deskLift(targetY: 790, screenHeight: 800, covered: 300), 300);
    expect(deskLift(targetY: 800, screenHeight: 800, covered: 300), 300);
    expect(deskLift(targetY: 810, screenHeight: 800, covered: 300), 0);
  });

  testWidgets(
    'fit to screen resets zoom locally even without display metadata',
    (tester) async {
      final rpc = await _pumpDeskPicture(tester);
      final picture = find.byKey(const Key('desk-picture'));
      final view = find.byKey(const Key('desk-toolbar-view'));
      final fit = tester.getRect(picture);
      await _expandToolbar(tester);
      expect(view, findsNothing);

      final left = await tester.startGesture(
        const Offset(370, 200),
        pointer: 1,
      );
      final right = await tester.startGesture(
        const Offset(430, 200),
        pointer: 2,
      );
      await left.moveBy(const Offset(-80, 0));
      await right.moveBy(const Offset(80, 0));
      await left.up();
      await right.up();
      await tester.pumpAndSettle();
      expect(tester.getSize(picture).width, greaterThan(fit.width));
      await tester.tap(view);
      await tester.pumpAndSettle();
      expect(find.text(zh.deskFitScreen), findsOneWidget);
      expect(find.text(zh.deskEntireScreen), findsNothing);
      await tester.tap(find.text(zh.deskFitScreen));
      await tester.pumpAndSettle();
      expect(tester.getRect(picture), fit);
      expect(find.text(zh.deskFitScreen), findsNothing);
      await _expandToolbar(tester);
      expect(view, findsNothing);
      expect(rpc.calls, ['desk.subscribe']);
      await tester.pumpWidget(const SizedBox.shrink());
      await _flushStreamCleanup(tester);
      expect(rpc.calls, ['desk.subscribe', 'desk.unsubscribe']);
    },
  );

  for (final trackpad in [true, false]) {
    testWidgets('keyboard reveals the ${trackpad ? 'cursor' : 'last tap'}', (
      tester,
    ) async {
      await _pumpDeskPicture(tester, trackpad: trackpad);
      final picture = find.byKey(const Key('desk-picture'));
      final before = tester.getRect(picture);
      if (trackpad) {
        await tester.tapAt(const Offset(400, 190));
        await tester.dragFrom(const Offset(400, 190), const Offset(0, 150));
      } else {
        await tester.tapAt(const Offset(400, 340));
      }
      await _expandToolbar(tester);
      await tester.tap(find.byKey(const Key('desk-toolbar-keyboard')));
      tester.view.viewInsets = const FakeViewPadding(bottom: 110);
      await tester.pumpAndSettle();
      final raised = tester.getRect(picture);
      expect(raised.top, lessThan(before.top - 70));
      expect(raised.size, before.size);

      if (trackpad) {
        await tester.dragFrom(const Offset(400, 210), const Offset(0, -120));
        await tester.pumpAndSettle();
        expect(tester.getRect(picture).top, greaterThan(raised.top + 50));
      }

      tester.view.viewInsets = FakeViewPadding.zero;
      await tester.pumpAndSettle();
      expect(tester.getRect(picture), before);
      await tester.pumpWidget(const SizedBox.shrink());
      await _flushStreamCleanup(tester);
    });
  }
}

Future<void> _flushStreamCleanup(WidgetTester tester) async {
  // 让取消广播监听的 Future 和测试帧里的延续交替跑完。
  for (var turn = 0; turn < 4; turn++) {
    await tester.pump();
    await tester.runAsync(() => Future<void>.delayed(Duration.zero));
  }
  await tester.pump();
}

Future<_DeskRpc> _pumpDeskPicture(
  WidgetTester tester, {
  bool trackpad = true,
}) async {
  addTearDown(tester.view.reset);
  _surface(tester, const Size(800, 400), FakeViewPadding.zero);
  _mockDeskChannels(tester);
  final rpc = _DeskRpc();
  final receiver = _DeskReceiver();
  final model = _DeskModel(rpc);
  addTearDown(model.dispose);
  addTearDown(receiver.frames.close);
  addTearDown(rpc.frames.close);
  model.updateSettings((s) {
    s.deskGesturesSeen = true;
    s.deskTrackpad = trackpad;
  });
  await tester.pumpWidget(
    MaterialApp(
      theme: posseTheme(Brightness.dark),
      home: DeskPage(model: model, receiver: receiver),
    ),
  );
  await tester.pump();
  expect(receiver.frames.hasListener, isTrue);
  receiver.frames.add(const DeskStats(video: true, width: 1600, height: 900));
  await tester.pumpAndSettle();
  return rpc;
}

class _DeskReceiver extends DeskReceiver {
  final frames = StreamController<DeskStats>.broadcast();
  var inputs = 0;

  @override
  Stream<DeskStats> stats() => frames.stream;

  @override
  Future<void> sendInput(Uint8List plain) async {
    inputs++;
  }

  @override
  Future<void> stop() async {}
}

class _DeskRpc implements PhoneRpc {
  final frames = StreamController<Map<String, Object?>>.broadcast();
  final calls = <String>[];
  var sendEndOnUnsubscribe = true;

  @override
  Future<RpcStream> subscribe(
    String method,
    Map<String, Object?> params,
  ) async {
    calls.add(method);
    return RpcStream(method, frames.stream, () async {});
  }

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    calls.add(method);
    if (method == 'desk.unsubscribe' && sendEndOnUnsubscribe) {
      frames.add({'type': 'end', 'reason': 'unsubscribed'});
    }
    return {};
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

class _DeskModel extends PosseAppModel {
  _DeskModel(this.rpc)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());

  @override
  final PhoneRpc rpc;

  @override
  String get connectedUrl => 'ws://127.0.0.1:6789';
}

DeskToolbar _toolbar({VoidCallback? onSettings, bool landscape = false}) =>
    DeskToolbar(
      landscape: landscape,
      expanded: true,
      keyboardUp: false,
      shortcuts: false,
      onToggle: () {},
      onOrientation: () {},
      onKeyboard: () {},
      onShortcuts: () {},
      onSettings: onSettings ?? () {},
      onView: () {},
    );

Future<void> _expandToolbar(WidgetTester tester) async {
  await tester.tap(find.byKey(const Key('desk-toolbar-toggle')));
  await tester.pumpAndSettle();
}

void _surface(WidgetTester tester, Size size, FakeViewPadding inset) {
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1;
  tester.view.viewPadding = inset;
  tester.view.padding = inset;
}

void _mockDeskChannels(
  WidgetTester tester, {
  List<List<String>>? orientations,
  List<bool>? edges,
}) {
  tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
    SystemChannels.platform,
    (call) async {
      if (call.method == 'SystemChrome.setPreferredOrientations' &&
          orientations != null) {
        final args = call.arguments as List<dynamic>;
        orientations.add([for (final item in args) '$item']);
      }
      return null;
    },
  );
  // iOS 上方向走插件：锁记成要锁的方向，放开记成空列表，和 SystemChrome 的记法一样
  const orientation = MethodChannel('posse/desk/orientation');
  tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(orientation, (
    call,
  ) async {
    if (orientations == null) return null;
    if (call.method == 'lock') {
      orientations.add([
        for (final item in deskLockedOrientations(call.arguments == true))
          '$item',
      ]);
    } else if (call.method == 'release') {
      orientations.add([]);
    }
    return null;
  });
  const channel = MethodChannel('posse/desk/edges');
  tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(channel, (
    call,
  ) async {
    if (call.method == 'set' && edges != null) {
      edges.add(call.arguments == true);
    }
    return null;
  });
  addTearDown(() {
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
      SystemChannels.platform,
      null,
    );
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
      channel,
      null,
    );
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
      orientation,
      null,
    );
  });
}
