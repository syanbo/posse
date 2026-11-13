import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/desk/settings_sheet.dart';
import 'package:posse_mobile/desk/unlock.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/ui/widgets.dart';

Future<Future<bool?> Function()> _open(
  WidgetTester tester,
  PosseAppModel model, {
  Future<void> Function()? forget,
}) async {
  late Future<bool?> result;
  await tester.pumpWidget(
    MaterialApp(
      theme: posseTheme(Brightness.dark),
      home: Scaffold(
        body: Builder(
          builder: (context) => TextButton(
            onPressed: () {
              result = showDeskSettings(
                context,
                model,
                forgetPasswords: forget ?? () async {},
              );
            },
            child: const Text('open'),
          ),
        ),
      ),
    ),
  );
  await tester.tap(find.text('open'));
  await tester.pumpAndSettle();
  return () => result;
}

void main() {
  const channel = MethodChannel('posse/desk/password');
  var count = 0;
  var forgetCalls = 0;
  late MemorySettingsStore settingsStore;
  late PosseAppModel model;

  setUp(() {
    count = 0;
    forgetCalls = 0;
    settingsStore = MemorySettingsStore();
    model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: settingsStore,
    );
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(channel, (call) async {
          if (call.method == 'count') return count;
          if (call.method == 'forgetAll') {
            forgetCalls++;
            count = 0;
          }
          return null;
        });
  });

  tearDown(() {
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(channel, null);
    model.dispose();
  });

  testWidgets('mode changes persist and follow model changes while open', (
    tester,
  ) async {
    model.settings.deskGesturesSeen = true;
    await _open(tester, model);
    await tester.tap(find.text('触控板'));
    await tester.pumpAndSettle();
    expect(model.settings.deskGesturesSeen, isTrue);
    await tester.tap(find.text('直接点按'));
    await tester.pumpAndSettle();
    expect(model.settings.deskTrackpad, isFalse);
    expect(model.settings.deskGesturesSeen, isFalse);
    final saved = AppSettings.decode(settingsStore.data);
    expect(saved.deskTrackpad, isFalse);
    expect(saved.deskGesturesSeen, isFalse);
    expect(tester.widget<Segmented>(find.byType(Segmented)).selected, 1);
    model.updateSettings((s) => s.deskTrackpad = true);
    await tester.pumpAndSettle();
    expect(tester.widget<Segmented>(find.byType(Segmented)).selected, 0);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets('forget uses the page callback and reloads the native count', (
    tester,
  ) async {
    count = 2;
    final pending = Completer<void>();
    var callbacks = 0;
    await _open(
      tester,
      model,
      forget: () async {
        callbacks++;
        await pending.future;
        await const DeskPasswordStore().forgetAll();
      },
    );
    expect(find.text('记着 2 台电脑的登录密码'), findsOneWidget);
    await tester.tap(find.text('全部忘掉'));
    await tester.pump();
    final button = find.widgetWithText(TextButton, '全部忘掉');
    expect(tester.widget<TextButton>(button).onPressed, isNull);
    expect(callbacks, 1);
    pending.complete();
    await tester.pumpAndSettle();
    expect(forgetCalls, 1);
    expect(find.text('没有记住的密码'), findsOneWidget);
    expect(tester.widget<TextButton>(button).onPressed, isNull);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets('gesture help closes the sheet with true', (tester) async {
    final result = await _open(tester, model);
    await tester.tap(find.text('手势'));
    await tester.pumpAndSettle();
    expect(await result(), isTrue);
    expect(find.byType(Segmented), findsNothing);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets('ordinary dismissal does not request gesture help', (
    tester,
  ) async {
    final result = await _open(tester, model);
    await tester.tapAt(const Offset(10, 10));
    await tester.pumpAndSettle();
    expect(await result(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets('landscape with a keyboard keeps the entire sheet reachable', (
    tester,
  ) async {
    tester.view
      ..physicalSize = const Size(800, 400)
      ..devicePixelRatio = 1
      ..viewInsets = const FakeViewPadding(bottom: 180);
    addTearDown(tester.view.reset);
    count = 1;
    final result = await _open(
      tester,
      model,
      forget: () => const DeskPasswordStore().forgetAll(),
    );
    await tester.scrollUntilVisible(find.text('直接点按'), -100);
    await tester.tap(find.text('直接点按'));
    await tester.pumpAndSettle();
    expect(model.settings.deskTrackpad, isFalse);
    await tester.scrollUntilVisible(find.text('全部忘掉'), 100);
    await tester.tap(find.text('全部忘掉'));
    await tester.pumpAndSettle();
    expect(forgetCalls, 1);
    expect(find.text('没有记住的密码'), findsOneWidget);
    await tester.scrollUntilVisible(find.text('手势'), -100);
    await tester.tap(find.text('手势'));
    await tester.pumpAndSettle();
    expect(await result(), isTrue);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox.shrink());
  });
}
