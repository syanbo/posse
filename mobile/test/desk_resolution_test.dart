import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/desk/display_picker.dart';
import 'package:posse_mobile/desk/displays.dart';
import 'package:posse_mobile/desk/frame_rate.dart';
import 'package:posse_mobile/desk/resolution.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/ui/theme.dart';

final _displays = DeskDisplays.parse({
  'current': 1,
  'displays': [
    {
      'id': 1,
      'name': 'Display',
      'x': 0,
      'y': 0,
      'w': 2560,
      'h': 1600,
      'pw': 2560,
      'ph': 1600,
    },
  ],
});

Future<void> _openPicker(
  WidgetTester tester, {
  int? resolution,
  ValueNotifier<int>? generation,
  Future<void> Function(int)? onResolution,
  Brightness brightness = Brightness.dark,
  Locale locale = const Locale('zh'),
  double textScale = 1,
}) async {
  await tester.pumpWidget(
    MaterialApp(
      theme: posseTheme(brightness),
      locale: locale,
      localizationsDelegates: AppLocalizations.localizationsDelegates,
      supportedLocales: AppLocalizations.supportedLocales,
      builder: (context, child) => MediaQuery(
        data: MediaQuery.of(context)
            .copyWith(textScaler: TextScaler.linear(textScale)),
        child: child!,
      ),
      home: Builder(
        builder: (context) => TextButton(
          onPressed: () => pickDeskView(
            context,
            _displays,
            windows: Future.value(const <DeskWindow>[]),
            resolution: resolution,
            generation: generation,
            onResolution: onResolution,
            fps: 30,
            onFps: (_) {},
          ),
          child: const Text('open'),
        ),
      ),
    ),
  );
  await tester.tap(find.text('open'));
  await tester.pumpAndSettle();
}

Future<void> _chooseResolution(WidgetTester tester, String label) async {
  await tester.tap(find.byKey(const Key('desk-resolution')));
  await tester.pumpAndSettle();
  final option = find.descendant(
    of: find.byKey(const Key('desk-stream-options')),
    matching: find.text(label),
  );
  await tester.ensureVisible(option);
  await tester.tap(option);
  await tester.pumpAndSettle();
}

void main() {
  test('resolution survives storage and reconnect, independently for each computer', () async {
    final store = MemorySettingsStore();
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: store,
    );
    addTearDown(model.dispose);
    final choice = DeskResolutionChoice();
    model.settings.deskFps['one'] = 30;
    await choice.choose(model, 'one', 1440);
    await choice.choose(model, 'two', 720);
    final restored = AppSettings.decode(store.data);
    expect(restored.deskResolution, {'one': 1440, 'two': 720});
    expect(restored.deskFps, {'one': 30});
    expect(deskSubscribeParams(restored, 'one', width: 800, height: 600), {
      'width': 800,
      'height': 600,
      'codecs': ['hevc'],
      'fps': 30,
      'resolution': 1440,
    });
    expect(deskResolutionOffer(false, restored, 'one'), isNull);
    expect(restored.deskResolution['one'], 1440);
    expect(deskResolutionOffer(true, restored, 'one'), 1440);
    expect(deskResolutionOffer(true, restored, 'other'), 0);
    expect(
      deskSubscribeParams(
        restored,
        'other',
        width: 800,
        height: 600,
      ).containsKey('resolution'),
      isFalse,
    );
    await choice.choose(model, 'one', 0);
    expect(model.settings.deskResolution, {'two': 720});
    expect(model.settings.deskFps['one'], 30);
  });

  test(
    'unknown stored resolutions are ignored without dropping other settings',
    () {
      final settings = AppSettings.decode(
        jsonEncode({
          'deskResolution': {
            'a': 2160,
            'b': '720',
            'c': 720,
            'd': 1080.0,
            'e': 0,
          },
          'phoneId': 'kept',
        }),
      );
      expect(settings.deskResolution, {'c': 720});
      expect(settings.phoneId, 'kept');
    },
  );

  test(
    'rapid choices coalesce and an error does not discard the final choice',
    () async {
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
      );
      addTearDown(model.dispose);
      final choice = DeskResolutionChoice();
      final gates = <Completer<Object?>>[];
      final sent = <int>[];
      Future<Object?> call(String method, Map<String, Object?> params) {
        expect(method, 'desk.resolution');
        sent.add(params['resolution']! as int);
        final gate = Completer<Object?>();
        gates.add(gate);
        return gate.future;
      }

      final first = choice.choose(model, 'one', 720, call: call);
      final firstError = expectLater(first, throwsA(isA<PhoneError>()));
      final second = choice.choose(model, 'one', 1080, call: call);
      final last = choice.choose(model, 'one', 1440, call: call);
      expect(model.settings.deskResolution['one'], 1440);
      await Future<void>.delayed(Duration.zero);
      expect(sent, [720]);
      gates[0].completeError(const PhoneError(PhoneError.notConnected));
      await firstError;
      await Future<void>.delayed(Duration.zero);
      expect(sent, [720, 1440]);
      await second;
      gates[1].complete(null);
      await last;
      expect(model.settings.deskResolution['one'], 1440);
    },
  );

  test(
    'a successful in-flight choice sends only the latest pending value',
    () async {
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
      );
      addTearDown(model.dispose);
      final choice = DeskResolutionChoice();
      final first = Completer<Object?>();
      final sent = <int>[];
      Future<Object?> call(String _, Map<String, Object?> params) {
        sent.add(params['resolution']! as int);
        return sent.length == 1 ? first.future : Future.value();
      }

      final one = choice.choose(model, 'one', 720, call: call);
      final skipped = choice.choose(model, 'one', 1080, call: call);
      final last = choice.choose(model, 'one', 1440, call: call);
      await skipped;
      expect(sent, [720]);
      first.complete(null);
      await Future.wait([one, last]);
      expect(sent, [720, 1440]);
    },
  );

  test(
    'changing watch or disposing drops pending callbacks and old failures',
    () async {
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
      );
      addTearDown(model.dispose);
      final choice = DeskResolutionChoice();
      final old = Completer<Object?>();
      final sent = <String>[];
      Future<Object?> oldCall(String _, Map<String, Object?> params) {
        sent.add('old:${params['resolution']}');
        return old.future;
      }

      final first = choice.choose(model, 'old', 720, call: oldCall);
      final dropped = choice.choose(model, 'old', 1440, call: oldCall);
      choice.cancelPending();
      await dropped;
      final current = choice.choose(
        model,
        'new',
        1080,
        call: (_, params) async {
          sent.add('new:${params['resolution']}');
          return null;
        },
      );
      old.completeError(TimeoutException('old'));
      await Future.wait([first, current]);
      expect(sent, ['old:720', 'new:1080']);
      final active = Completer<Object?>();
      final activeDone = choice.choose(
        model,
        'new',
        720,
        call: (_, _) => active.future,
      );
      final pending = choice.choose(
        model,
        'new',
        1440,
        call: (_, _) async {
          fail('disposed request was sent');
        },
      );
      choice.dispose();
      await pending;
      active.completeError(TimeoutException('disposed'));
      await activeDone;
    },
  );

  testWidgets(
    'watch generation change closes the old sheet and a new sheet works',
    (tester) async {
      final generation = ValueNotifier<int>(0);
      addTearDown(generation.dispose);
      final old = Completer<void>();
      final selected = <int>[];
      await _openPicker(
        tester,
        resolution: 0,
        generation: generation,
        onResolution: (value) {
          selected.add(value);
          return selected.length == 1 ? old.future : Future.value();
        },
      );
      await _chooseResolution(tester, '720p');
      await tester.pump();
      await tester.tap(find.byKey(const Key('desk-resolution')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('desk-stream-options')), findsOneWidget);
      generation.value++;
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('desk-resolution')), findsNothing);
      expect(find.byKey(const Key('desk-stream-options')), findsNothing);
      await tester.tap(find.text('open'));
      await tester.pumpAndSettle();
      old.completeError(const PhoneError(PhoneError.notConnected));
      await tester.pumpAndSettle();
      await _chooseResolution(tester, '1080p');
      await tester.pumpAndSettle();
      expect(selected, [720, 1080]);
      expect(find.byKey(const Key('desk-resolution')), findsOneWidget);
      expect(find.byKey(const Key('desk-resolution-error')), findsNothing);
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets(
    'old computers hide resolution while keeping frame rate available',
    (tester) async {
      await _openPicker(tester);
      expect(find.byKey(const Key('desk-resolution')), findsNothing);
      expect(find.byKey(const Key('desk-fps')), findsOneWidget);
    },
  );

  for (final brightness in Brightness.values) {
    for (final language in ['zh', 'en']) {
      testWidgets(
        'resolution sheet fits narrow $brightness $language and stays open',
        (tester) async {
          tester.view.physicalSize = const Size(320, 700);
          tester.view.devicePixelRatio = 1;
          addTearDown(tester.view.resetPhysicalSize);
          addTearDown(tester.view.resetDevicePixelRatio);
          final values = <int>[];
          await _openPicker(
            tester,
            brightness: brightness,
            locale: Locale(language),
            resolution: 1080,
            onResolution: (v) async {
              values.add(v);
            },
          );
          final row = find.byKey(const Key('desk-resolution'));
          final colors = PosseColors.of(tester.element(row));
          Color? color(String label) => tester
              .widget<Text>(
                find.descendant(of: row, matching: find.text(label)),
              )
              .style
              ?.color;
          expect(color('1080p'), colors.text);
          await _chooseResolution(tester, '1440p');
          await tester.pumpAndSettle();
          expect(values, [1440]);
          expect(row, findsOneWidget);
          expect(color('1440p'), colors.text);
          expect(tester.takeException(), isNull);
        },
      );
    }
  }

  for (final language in ['zh', 'en']) {
    for (final layout in [
      (size: const Size(390, 844), scale: 1.0),
      (size: const Size(844, 390), scale: 1.0),
      (size: const Size(320, 700), scale: 2.0),
    ]) {
      testWidgets('compact controls fit $language $layout', (tester) async {
        tester.view.physicalSize = layout.size;
        tester.view.devicePixelRatio = 1;
        addTearDown(tester.view.resetPhysicalSize);
        addTearDown(tester.view.resetDevicePixelRatio);
        await _openPicker(
          tester,
          resolution: 1080,
          onResolution: (_) async {},
          locale: Locale(language),
          textScale: layout.scale,
        );
        final quality = tester.getRect(
          find.byKey(const Key('desk-resolution')),
        );
        final fps = tester.getRect(find.byKey(const Key('desk-fps')));
        expect(quality.height, greaterThanOrEqualTo(44));
        expect(fps.height, greaterThanOrEqualTo(44));
        if (layout.scale == 1) {
          // 测试方块字体把英文画得更宽，英文竖屏允许按实际文字宽度换行。
          if (language == 'zh' || layout.size.width > layout.size.height) {
            expect(quality.top, fps.top);
          }
          expect(quality.height, lessThanOrEqualTo(52));
        } else {
          expect(fps.top, greaterThanOrEqualTo(quality.bottom));
        }
        expect(quality.overlaps(fps), isFalse);
        expect(fps.right, lessThanOrEqualTo(layout.size.width - 16));
        expect(find.text('720p'), findsNothing);
        await _chooseResolution(tester, '1440p');
        expect(find.text('1440p'), findsOneWidget);
        expect(find.text('30'), findsOneWidget);
        expect(find.byKey(const Key('desk-stream-options')), findsNothing);
        expect(tester.takeException(), isNull);
      });
    }
  }

  testWidgets('floating options animate without moving the sheet or display', (
    tester,
  ) async {
    final selected = <int>[];
    await _openPicker(
      tester,
      resolution: 0,
      onResolution: (value) async => selected.add(value),
    );
    final sheet = find.byType(BottomSheet);
    final display = find.byKey(const Key('desk-display-1'));
    final quality = find.byKey(const Key('desk-resolution'));
    final fps = find.byKey(const Key('desk-fps'));
    final before = [
      tester.getRect(sheet),
      tester.getRect(display),
      tester.getRect(quality),
      tester.getRect(fps),
    ];
    void staysStill() {
      expect([
        tester.getRect(sheet),
        tester.getRect(display),
        tester.getRect(quality),
        tester.getRect(fps),
      ], before);
    }

    await tester.tap(quality);
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 60));
    final fade = tester.widget<FadeTransition>(
      find
          .ancestor(
            of: find.byKey(const Key('desk-stream-options')),
            matching: find.byType(FadeTransition),
          )
          .first,
    );
    expect(fade.opacity.value, greaterThan(0));
    expect(fade.opacity.value, lessThan(1));
    staysStill();
    await tester.pumpAndSettle();
    staysStill();
    await tester.tap(fps);
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 60));
    staysStill();
    await tester.pumpAndSettle();
    await tester.tap(quality);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('desk-stream-option-1440')));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 40));
    staysStill();
    await tester.pumpAndSettle();
    staysStill();
    expect(selected, [1440]);
    expect(find.byKey(const Key('desk-stream-options')), findsNothing);

    await tester.tap(quality);
    await tester.pumpAndSettle();
    final dismiss = tester.getRect(
      find.byKey(const Key('desk-stream-dismiss')),
    );
    await tester.tapAt(dismiss.bottomCenter - const Offset(0, 8));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('desk-stream-options')), findsNothing);
    expect(sheet, findsOneWidget);
    staysStill();
    expect(tester.binding.hasScheduledFrame, isFalse);
  });

  testWidgets(
    'floating options switch directly and collapse without choosing',
    (tester) async {
      final selected = <int>[];
      await _openPicker(
        tester,
        resolution: 1080,
        onResolution: (value) async => selected.add(value),
      );
      await tester.tap(find.byKey(const Key('desk-resolution')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('desk-stream-option-720')), findsOneWidget);
      await tester.tap(find.byKey(const Key('desk-fps')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('desk-stream-option-720')), findsNothing);
      expect(find.byKey(const Key('desk-stream-option-60')), findsOneWidget);
      await tester.tap(find.byKey(const Key('desk-fps')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('desk-stream-options')), findsNothing);
      expect(selected, isEmpty);
      expect(find.byType(BottomSheet), findsOneWidget);
    },
  );

  testWidgets(
    'failure is visible, preference is retained, and the same choice can retry',
    (tester) async {
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
      );
      addTearDown(model.dispose);
      final choice = DeskResolutionChoice();
      var fail = true;
      await _openPicker(
        tester,
        resolution: 0,
        onResolution: (value) => choice.choose(
          model,
          'one',
          value,
          call: (_, _) async {
            if (fail) throw const PhoneError(PhoneError.notConnected);
            return null;
          },
        ),
      );
      await _chooseResolution(tester, '720p');
      await tester.pumpAndSettle();
      expect(model.settings.deskResolution['one'], 720);
      expect(find.byKey(const Key('desk-resolution-error')), findsOneWidget);
      final context = tester.element(find.byKey(const Key('desk-resolution')));
      expect(find.text(context.l10n.errorNotConnected), findsOneWidget);
      fail = false;
      await _chooseResolution(tester, '720p');
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('desk-resolution-error')), findsNothing);
      expect(find.byKey(const Key('desk-resolution')), findsOneWidget);
    },
  );

  testWidgets(
    'late failure from an older choice does not cover the final selection',
    (tester) async {
      final old = Completer<void>();
      await _openPicker(
        tester,
        resolution: 0,
        onResolution: (value) => value == 720 ? old.future : Future.value(),
      );
      await _chooseResolution(tester, '720p');
      await tester.pump();
      await _chooseResolution(tester, '1440p');
      await tester.pump();
      old.completeError(const PhoneError(PhoneError.notConnected));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('desk-resolution-error')), findsNothing);
    },
  );
}
