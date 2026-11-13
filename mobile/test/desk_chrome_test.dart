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
import 'package:posse_mobile/desk/toolbar.dart';
import 'package:posse_mobile/l10n/l10n.dart';
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

  test(
    'toolbar positions round-trip separately and invalid values fall back',
    () {
      final settings = AppSettings(
        deskToolbarPortrait: const Offset(0.3, 0.7),
        deskToolbarLandscape: const Offset(0.8, 0.4),
      );
      final back = AppSettings.decode(jsonEncode(settings.toJson()));
      expect(back.deskToolbarPortrait, const Offset(0.3, 0.7));
      expect(back.deskToolbarLandscape, const Offset(0.8, 0.4));
      final invalid = AppSettings.decode(
        jsonEncode({
          'deskToolbarPortrait': [-2, 3],
          'deskToolbarLandscape': ['bad', 0],
        }),
      );
      expect(invalid.deskToolbarPortrait, const Offset(0, 1));
      expect(invalid.deskToolbarLandscape, const Offset(1, 0));
      expect(AppSettings.decode('{}').deskToolbarPortrait, const Offset(1, 0));
    },
  );

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

  testWidgets('toolbar stays open and header controls are separate', (
    tester,
  ) async {
    var backs = 0;
    var helps = 0;
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: Scaffold(
          body: Column(
            children: [
              Row(
                mainAxisAlignment: MainAxisAlignment.spaceBetween,
                children: [
                  DeskBackButton(onBack: () => backs++),
                  const DeskStatusPill(
                    latency: Duration(milliseconds: 40),
                    unresponsive: false,
                    fps: 60,
                    video: true,
                  ),
                ],
              ),
              _toolbar(onSettings: () => helps++),
            ],
          ),
        ),
      ),
    );
    await tester.pump(const Duration(seconds: 10));
    expect(find.text('60 fps'), findsOneWidget);
    expect(find.byKey(const Key('desk-toolbar-orientation')), findsOneWidget);
    expect(
      find.descendant(
        of: find.byKey(const Key('desk-toolbar')),
        matching: find.byKey(const Key('desk-toolbar-back')),
      ),
      findsNothing,
    );
    await tester.tap(find.byKey(const Key('desk-toolbar-back')));
    expect(backs, 1);
    await tester.tap(find.byKey(const Key('desk-toolbar-settings')));
    expect(helps, 1);
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
                  anchor: ValueNotifier<Offset>(const Offset(0.5, 0)),
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
                keyboardUp: false,
                shortcuts: false,
                debugOpen: false,
                stopped: false,
                onOrientation: () => turns++,
                onKeyboard: () {},
                onShortcuts: () {},
                onDebug: () {},
                onSettings: () {},
                onDisconnect: () {},
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
    final anchor = ValueNotifier<Offset>(const Offset(0.5, 0));
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
    final start = anchor.value.dx;
    await finger.moveBy(const Offset(24, 0));
    await tester.pump();
    expect(anchor.value.dx - start, closeTo(24 / (300 - 200), 0.05));
    final beforeBatch = anchor.value.dx;
    await finger.moveBy(const Offset(-5, 0));
    await finger.moveBy(const Offset(-5, 0));
    await tester.pump();
    expect(anchor.value.dx - beforeBatch, closeTo(-10 / (300 - 200), 0.001));
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

      nav.pop();
      await tester.pumpAndSettle();
      expect(orientations.last, isEmpty);
      expect(edges.last, isFalse);
    },
  );

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
      final first = bar();
      final backAt = back();
      final statusAt = status();
      expect(first.right, 392);
      expect(first.top, greaterThan(backAt.bottom));
      expect(first.height, greaterThan(first.width));
      await tester.drag(
        find.byKey(const Key('desk-toolbar-handle')),
        const Offset(-220, 300),
      );
      await tester.pumpAndSettle();
      final moved = bar();
      expect(moved.left, greaterThan(50));
      expect(moved.right, lessThan(300));
      expect(moved.top, greaterThan(first.top + 200));
      expect(back(), backAt);
      expect(status(), statusAt);
      final saved = model.settings.deskToolbarPortrait;
      expect(AppSettings.decode(store.data).deskToolbarPortrait, saved);

      // 键盘只推开实际被遮住的工具条，不改变偏好；固定控件也不跟着动。
      tester.view.viewInsets = const FakeViewPadding(bottom: 450);
      await tester.pumpAndSettle();
      expect(bar().bottom, lessThanOrEqualTo(342));
      expect(bar().width, greaterThan(bar().height));
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
      expect(bar().width, greaterThan(bar().height));
      await tester.drag(
        find.byKey(const Key('desk-toolbar-handle')),
        const Offset(-180, 100),
      );
      await tester.pumpAndSettle();
      final side = bar();
      expect(model.settings.deskToolbarLandscape, isNot(const Offset(1, 0)));

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
        final anchor = ValueNotifier(const Offset(0.7, 0.2));
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
    final anchor = ValueNotifier(const Offset(1, 0));
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
      find.byKey(const Key('desk-toolbar-handle')),
      const Offset(-30, 24),
    );
    await tester.pumpAndSettle();
    expect(anchor.value.dx, 1);
    expect(anchor.value.dy, greaterThan(0));
    await pump(500);
    await tester.pumpAndSettle();
    expect(tester.getRect(find.byKey(const Key('desk-toolbar'))).right, 500);
  });

  for (final (screen, keyboard) in [
    (const Size(568, 320), 250.0),
    (const Size(320, 568), 470.0),
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
      final disconnect = rect('desk-toolbar-disconnect');
      expect(disconnect.left, greaterThanOrEqualTo(bar.left));
      expect(disconnect.right, lessThanOrEqualTo(bar.right));
      expect(tester.takeException(), isNull);
      tester.view.viewInsets = FakeViewPadding.zero;
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('desk-toolbar-handle')), findsOneWidget);
      expect(model.settings.deskToolbarPortrait, const Offset(1, 0));
      expect(model.settings.deskToolbarLandscape, const Offset(1, 0));
      await tester.pumpWidget(const SizedBox.shrink());
      await tester.pumpAndSettle();
    });
  }

  testWidgets('landscape debug panel clears the default horizontal toolbar', (
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
    await tester.tap(find.byKey(const Key('desk-toolbar-debug')));
    await tester.pumpAndSettle();
    final bar = tester.getRect(find.byKey(const Key('desk-toolbar')));
    final panel = tester.getRect(find.byKey(const Key('desk-debug-panel')));
    expect(panel.top, greaterThan(bar.bottom));
    expect(bar.overlaps(panel), isFalse);
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
    expect(deskLift(tapY: null, screenHeight: 800, covered: 300), 0);
    // 点在露着的地方不动
    expect(deskLift(tapY: 200, screenHeight: 800, covered: 300), 0);
    // 点在会被盖住的地方：露到盖住的边上面 48
    expect(deskLift(tapY: 600, screenHeight: 800, covered: 300), 148);
    // 最多移盖住的那么高
    expect(deskLift(tapY: 790, screenHeight: 800, covered: 300), 300);
  });
}

DeskToolbar _toolbar({VoidCallback? onSettings, bool landscape = false}) =>
    DeskToolbar(
      landscape: landscape,
      keyboardUp: false,
      shortcuts: false,
      debugOpen: false,
      stopped: false,
      onOrientation: () {},
      onKeyboard: () {},
      onShortcuts: () {},
      onDebug: () {},
      onSettings: onSettings ?? () {},
      onDisconnect: () {},
      onView: () {},
    );

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
