import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/app_state.dart';
import 'package:hive_mobile/desk/display_picker.dart';
import 'package:hive_mobile/desk/displays.dart';
import 'package:hive_mobile/ui/theme.dart';

// 内建屏在左，一块 4K 外接屏在右边、顶端对齐，和电脑报来的一样
final _frame = <String, Object?>{
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
    {
      'id': 7,
      'name': '',
      'x': 1512,
      'y': 0,
      'w': 1920,
      'h': 1080,
      'pw': 3840,
      'ph': 2160,
    },
    {'id': 0, 'name': 'bad'},
  ],
};

void main() {
  test('displays parse, skip bad entries and name the unnamed', () {
    final d = DeskDisplays.parse(_frame);
    expect(d.list.map((x) => x.id), [1, 7]);
    expect(d.current, 1);
    expect(d.several, isTrue);
    expect(d.list.first.main, isTrue);
    expect(d.list.last.rect, const Rect.fromLTWH(1512, 0, 1920, 1080));
    expect(deskDisplayName(d, d.list.first), 'Built-in Retina Display');
    expect(deskDisplayName(d, d.list.last), '显示器 2');
    expect(DeskDisplays.parse(const {}).several, isFalse);
  });

  test('the map keeps the arrangement and fits the box', () {
    final d = DeskDisplays.parse(_frame);
    final rects = deskDisplayLayout(d.list, const Size(300, 120));
    final a = rects[1]!;
    final b = rects[7]!;
    // 并排、顶端对齐、比例不变，整体在框里居中
    expect(a.right, closeTo(b.left, 1e-9));
    expect(a.top, closeTo(b.top, 1e-9));
    expect(b.width / a.width, closeTo(1920 / 1512, 1e-9));
    expect(a.left + (300 - b.right), closeTo(2 * a.left, 1e-9));
    expect(b.right, lessThanOrEqualTo(300 + 1e-9));
    expect(deskDisplayLayout(const [], const Size(300, 120)), isEmpty);
  });

  test('the chosen display is remembered per computer', () {
    final s = AppSettings()..deskDisplays['pk1'] = 7;
    final back = AppSettings.decode(jsonEncode(s.toJson()));
    expect(back.deskDisplays, {'pk1': 7});
    expect(
      AppSettings.decode(
        jsonEncode({
          'deskDisplays': {'a': 'x', 'b': 3},
        }),
      ).deskDisplays,
      {'b': 3},
    );
  });

  testWidgets('tapping a display on the map or in the list picks it', (
    tester,
  ) async {
    final d = DeskDisplays.parse(_frame);
    int? picked;
    await tester.pumpWidget(
      MaterialApp(
        theme: hiveTheme(Brightness.dark),
        home: Builder(
          builder: (context) => TextButton(
            onPressed: () async => picked = await pickDeskDisplay(context, d),
            child: const Text('open'),
          ),
        ),
      ),
    );
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    expect(find.text('Built-in Retina Display'), findsOneWidget);
    expect(find.text('3840 × 2160'), findsOneWidget);
    expect(find.text('3024 × 1964 · 主屏'), findsOneWidget);
    await tester.tap(find.byKey(const Key('desk-display-map-7')));
    await tester.pumpAndSettle();
    expect(picked, 7);

    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('desk-display-1')));
    await tester.pumpAndSettle();
    expect(picked, 1);
  });

  // 横屏的 iPhone：高度只有 390，面板要能滚，不能溢出
  testWidgets('the picker fits a phone held sideways', (tester) async {
    tester.view.physicalSize = const Size(2532, 1170);
    tester.view.devicePixelRatio = 3;
    addTearDown(tester.view.reset);
    final d = DeskDisplays.parse(_frame);
    await tester.pumpWidget(
      MaterialApp(
        theme: hiveTheme(Brightness.dark),
        home: Builder(
          builder: (context) => TextButton(
            onPressed: () => pickDeskDisplay(context, d),
            child: const Text('open'),
          ),
        ),
      ),
    );
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);
    expect(find.byKey(const Key('desk-display-map-1')), findsOneWidget);
  });
}
