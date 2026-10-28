import 'dart:convert';

import 'package:flutter/material.dart';
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
import 'package:posse_mobile/ui/theme.dart';

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
    expect(deskBarOnRight(left: 59, right: 0), isTrue);
    expect(deskBarOnRight(left: 0, right: 59), isFalse);
    expect(deskBarOnRight(left: 0, right: 0), isTrue);
  });

  test('the toolbar collapses after it has been left alone', () {
    final chrome = DeskChrome();
    chrome.toggleExpanded(Duration.zero);
    expect(chrome.expanded, isTrue);
    chrome.noteActivity(const Duration(seconds: 2));
    expect(chrome.collapseIfIdle(const Duration(seconds: 5)), isFalse);
    expect(
      chrome.collapseIfIdle(const Duration(seconds: 2) + deskBarIdle),
      isTrue,
    );
    expect(chrome.expanded, isFalse);
    expect(chrome.collapseIfIdle(const Duration(seconds: 30)), isFalse);
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
    expect(deskBarAlignment(0.5), 0);
    expect(deskBarAlignment(0), -1);
    expect(deskBarTrack(390, 280), 110);
    expect(deskFpsLabel(59.6, video: true), '60 fps');
    expect(deskFpsLabel(0, video: false), '— fps');
    expect(deskFpsLabel(-1, video: true), '— fps');
    expect(deskFpsLabel(0, video: true, still: true), '静止');
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

  testWidgets('the toolbar shows fps until it is opened', (tester) async {
    var backs = 0;
    var helps = 0;
    Future<void> pump(bool expanded, {bool video = true}) {
      return tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          home: Scaffold(
            body: Center(
              child: DeskToolbar(
                expanded: expanded,
                latency: const Duration(milliseconds: 40),
                unresponsive: false,
                fps: 60,
                video: video,
                landscape: false,
                keyboardUp: false,
                shortcuts: false,
                debugOpen: false,
                stopped: false,
                onToggle: () {},
                onBack: () => backs++,
                onOrientation: () {},
                onKeyboard: () {},
                onShortcuts: () {},
                onDebug: () {},
                onHelp: () => helps++,
                onDisconnect: () {},
              ),
            ),
          ),
        ),
      );
    }

    await pump(false);
    expect(find.byKey(const Key('desk-fps')), findsOneWidget);
    expect(find.text('60 fps'), findsOneWidget);
    expect(find.byKey(const Key('desk-toolbar-back')), findsNothing);

    await pump(true);
    await tester.pump(const Duration(milliseconds: 200));
    expect(find.byKey(const Key('desk-toolbar-back')), findsOneWidget);
    expect(find.byKey(const Key('desk-toolbar-orientation')), findsOneWidget);
    await tester.tap(find.byKey(const Key('desk-toolbar-back')));
    expect(backs, 1);
    await tester.tap(find.byKey(const Key('desk-toolbar-help')));
    expect(helps, 1);

    // 还没画面（连接中、连不上）：不用点开就有返回和横屏
    await pump(false, video: false);
    await tester.pump(const Duration(milliseconds: 200));
    expect(find.byKey(const Key('desk-toolbar-back')), findsOneWidget);
    expect(find.byKey(const Key('desk-toolbar-orientation')), findsOneWidget);
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
                child: DeskBarSlot(
                  anchor: ValueNotifier<Offset>(const Offset(0.5, 0)),
                  onDrag: () {},
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
                expanded: true,
                latency: null,
                unresponsive: false,
                fps: 60,
                video: true,
                landscape: landscape,
                keyboardUp: false,
                shortcuts: false,
                debugOpen: false,
                stopped: false,
                onToggle: () {},
                onBack: () {},
                onOrientation: () => turns++,
                onKeyboard: () {},
                onShortcuts: () {},
                onDebug: () {},
                onHelp: () {},
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
                        onDrag: () {},
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
    await finger.up();
    await tester.pump();
    // 松手吸边
    expect(anchor.value.dx, 1);
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

  testWidgets('the ball drags anywhere and snaps to the nearer side', (
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
    _surface(
      tester,
      const Size(400, 800),
      const FakeViewPadding(top: 47, bottom: 34),
    );
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: DeskPage(model: model),
      ),
    );
    await tester.pumpAndSettle();
    Rect ball() => tester.getRect(find.byKey(const Key('desk-toolbar')));

    // 竖屏默认在右上角，按钮竖着排
    final first = ball();
    expect(first.right, greaterThan(390));
    expect(first.top, lessThan(80));
    expect(first.height, greaterThan(first.width));

    // 拖到左下偏中，松手吸到左边，高度留在放手的地方
    await tester.drag(
      find.byKey(const Key('desk-toolbar')),
      const Offset(-250, 300),
    );
    await tester.pumpAndSettle();
    final moved = ball();
    expect(moved.left, lessThan(10));
    expect(moved.top, greaterThan(first.top + 200));

    // 横屏：刘海在左，球在右边
    _surface(
      tester,
      const Size(800, 400),
      const FakeViewPadding(left: 59, bottom: 21),
    );
    await tester.pumpAndSettle();
    final side = ball();
    expect(side.right, greaterThan(790));
    expect(side.height, greaterThan(side.width));

    // 回竖屏，回到竖屏时放的位置
    _surface(
      tester,
      const Size(400, 800),
      const FakeViewPadding(top: 47, bottom: 34),
    );
    await tester.pumpAndSettle();
    expect(ball().left, closeTo(moved.left, 2));
    expect(ball().top, closeTo(moved.top, 2));

    await tester.pumpWidget(const SizedBox.shrink());
    await tester.pump();
  });

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
