import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/desk/display_picker.dart';
import 'package:posse_mobile/desk/displays.dart';
import 'package:posse_mobile/desk/frame_rate.dart';
import 'package:posse_mobile/ui/theme.dart';

final _displays = DeskDisplays.parse({
  'type': 'displays',
  'current': 1,
  'displays': [
    {
      'id': 1,
      'name': 'Built-in Retina Display',
      'x': 0,
      'y': 0,
      'w': 1512,
      'h': 982,
      'pw': 3024,
      'ph': 1964,
      'main': true,
    },
  ],
});

Future<void> _openPicker(
  WidgetTester tester, {
  int? fps,
  ValueChanged<int>? onFps,
}) async {
  await tester.pumpWidget(
    MaterialApp(
      theme: posseTheme(Brightness.dark),
      home: Builder(
        builder: (context) => TextButton(
          onPressed: () => pickDeskView(
            context,
            _displays,
            windows: Future.value(const <DeskWindow>[]),
            fps: fps,
            onFps: onFps,
          ),
          child: const Text('open'),
        ),
      ),
    ),
  );
  await tester.tap(find.text('open'));
  await tester.pumpAndSettle();
}

void main() {
  test('the frame rate row shows only for a computer that offers it', () {
    final s = AppSettings()..deskFps['pk1'] = 30;
    // ready 没带 fps（旧电脑）：不显示
    expect(deskFpsOffer(false, s, 'pk1'), isNull);
    expect(deskFpsOffer(true, s, 'pk1'), 30);
    // 没记过是自动
    expect(deskFpsOffer(true, s, 'pk2'), 0);
  });

  test('the subscription carries the saved frame rate, none means auto', () {
    final s = AppSettings()
      ..deskDisplays['pk1'] = 7
      ..deskFps['pk1'] = 15;
    expect(deskSubscribeParams(s, 'pk1', width: 2556, height: 1179), {
      'width': 2556,
      'height': 1179,
      'codecs': ['hevc'],
      'display': 7,
      'fps': 15,
    });
    final auto = deskSubscribeParams(s, 'pk2', width: 1, height: 2);
    expect(auto.containsKey('fps'), isFalse);
    expect(auto.containsKey('display'), isFalse);
  });

  test('the choice is remembered per computer and bad values are dropped', () {
    final s = AppSettings()
      ..deskFps['pk1'] = 60
      ..deskFps['pk2'] = 15;
    expect(AppSettings.decode(jsonEncode(s.toJson())).deskFps, {
      'pk1': 60,
      'pk2': 15,
    });
    expect(
      AppSettings.decode(
        jsonEncode({
          'deskFps': {'a': 45, 'b': '30', 'c': 30, 'd': 0},
        }),
      ).deskFps,
      {'c': 30},
    );
    // 小数写法的档位只丢这一项，后面的设置照常读
    final after = AppSettings.decode(
      '{"deskFps":{"pk":30.0},"phoneId":"kept","pushDone":true}',
    );
    expect(after.deskFps, isEmpty);
    expect(after.phoneId, 'kept');
    expect(after.pushDone, isTrue);
  });

  test(
    'choosing calls desk.fps and saves; auto forgets the computer',
    () async {
      final store = MemorySettingsStore();
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: store,
      );
      addTearDown(model.dispose);
      final calls = <String>[];
      Future<Object?> call(String method, Map<String, Object?> params) async {
        calls.add('$method ${jsonEncode(params)}');
        return const <String, Object?>{};
      }

      await chooseDeskFps(model, 'pk1', 30, call: call);
      expect(calls, ['desk.fps {"fps":30}']);
      expect(model.settings.deskFps, {'pk1': 30});
      expect(AppSettings.decode(store.data!).deskFps, {'pk1': 30});
      // 下次订阅带上
      expect(
        deskSubscribeParams(model.settings, 'pk1', width: 1, height: 1)['fps'],
        30,
      );

      await chooseDeskFps(model, 'pk1', 0, call: call);
      expect(calls.last, 'desk.fps {"fps":0}');
      expect(model.settings.deskFps, isEmpty);

      // 没在看（没有 call）只记下；电脑报错也照样记下、不往外抛
      await chooseDeskFps(model, 'pk1', 15);
      expect(model.settings.deskFps, {'pk1': 15});
      await chooseDeskFps(
        model,
        'pk1',
        60,
        call: (_, _) async => throw StateError('gone'),
      );
      expect(model.settings.deskFps, {'pk1': 60});
      // 认不出的值当自动
      await chooseDeskFps(model, 'pk1', 45, call: call);
      expect(calls.last, 'desk.fps {"fps":0}');
      expect(model.settings.deskFps, isEmpty);
    },
  );

  testWidgets('no frame rate row when the computer does not offer it', (
    tester,
  ) async {
    await _openPicker(tester);
    expect(find.byKey(const Key('desk-fps')), findsNothing);
    expect(find.text('帧率'), findsNothing);
  });

  testWidgets('the row shows the saved choice and picks stay in the sheet', (
    tester,
  ) async {
    final picked = <int>[];
    await _openPicker(tester, fps: 30, onFps: picked.add);
    expect(find.byKey(const Key('desk-fps')), findsOneWidget);
    expect(find.text('帧率'), findsOneWidget);
    expect(find.text('30'), findsOneWidget);
    expect(find.text('15'), findsNothing);
    Future<void> openMenu() async {
      await tester.tap(find.byKey(const Key('desk-fps')));
      await tester.pumpAndSettle();
    }

    await openMenu();
    for (final label in ['自动', '60', '30', '15']) {
      expect(
        find.descendant(
          of: find.byKey(const Key('desk-stream-options')),
          matching: find.text(label),
        ),
        findsOneWidget,
      );
    }
    final selected = tester.widget<TextButton>(
      find.byKey(const Key('desk-stream-option-30')),
    );
    final colors = PosseColors.of(
      tester.element(find.byKey(const Key('desk-fps'))),
    );
    expect(selected.style!.backgroundColor!.resolve({}), colors.tint);

    await tester.tap(find.text('15'));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('desk-stream-options')), findsNothing);
    expect(find.byKey(const Key('desk-fps')), findsOneWidget);
    expect(find.text('15'), findsOneWidget);
    // 再点同一档不重复发。
    await openMenu();
    await tester.tap(find.byKey(const Key('desk-stream-option-15')));
    await tester.pumpAndSettle();
    await openMenu();
    await tester.tap(find.text('自动'));
    await tester.pumpAndSettle();
    expect(picked, [15, 0]);
  });
}
