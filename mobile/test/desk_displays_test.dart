import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/desk/display_picker.dart';
import 'package:posse_mobile/desk/displays.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/ui/theme.dart';

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

final _rawWindows = [
  {
    'id': 42,
    'app': 'Visual Studio Code',
    'title': 'posse — README.md',
    'w': 1290,
    'h': 865,
  },
  {'id': 9, 'app': 'Simulator', 'w': 361, 'h': 835},
];

final _windows = DeskWindow.parseList({'windows': _rawWindows});

final zh = lookupAppLocalizations(const Locale('zh'));

void main() {
  test('displays parse, skip bad entries and name the unnamed', () {
    final d = DeskDisplays.parse(_frame);
    expect(d.list.map((x) => x.id), [1, 7]);
    expect(d.current, 1);
    expect(d.several, isTrue);
    expect(d.list.first.main, isTrue);
    expect(d.list.last.rect, const Rect.fromLTWH(1512, 0, 1920, 1080));
    expect(deskDisplayName(zh, d, d.list.first), 'Built-in Retina Display');
    expect(deskDisplayName(zh, d, d.list.last), '显示器 2');
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

  testWidgets('display names sit on the map and tapping a display picks it', (
    tester,
  ) async {
    final d = DeskDisplays.parse(_frame);
    DeskPick? picked;
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: Builder(
          builder: (context) => TextButton(
            onPressed: () async => picked = await pickDeskView(
              context,
              d,
              windows: Future.value(_windows),
            ),
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
    expect(picked, (window: false, id: 7));

    // 多块屏只有排布图，没有一行行的列表
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('desk-display-1')), findsNothing);
    expect(
      find.descendant(
        of: find.byKey(const Key('desk-display-map-1')),
        matching: find.text('Built-in Retina Display'),
      ),
      findsOneWidget,
    );
    await tester.tap(find.byKey(const Key('desk-display-map-1')));
    await tester.pumpAndSettle();
    expect(picked, (window: false, id: 1));

    // 窗口：有标题写标题、下面写 App；没标题写 App、下面写大小
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    expect(find.text('posse — README.md'), findsOneWidget);
    expect(find.text('Visual Studio Code'), findsOneWidget);
    expect(find.text('Simulator'), findsOneWidget);
    expect(find.text('361 × 835'), findsOneWidget);
    // 面板高度固定，窗口在下面要滚到才点得到
    await tester.ensureVisible(find.byKey(const Key('desk-window-42')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('desk-window-42')));
    await tester.pumpAndSettle();
    expect(picked, (window: true, id: 42));
  });

  testWidgets('one display, windows still loading, the watched window ticked', (
    tester,
  ) async {
    final one = DeskDisplays.parse({
      ..._frame,
      'displays': [(_frame['displays']! as List).first],
      'window': {'id': 42, 'app': 'Visual Studio Code'},
    });
    expect(one.window?.id, 42);
    final windows = Completer<List<DeskWindow>>();
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: Builder(
          builder: (context) => TextButton(
            onPressed: () =>
                pickDeskView(context, one, windows: windows.future),
            child: const Text('open'),
          ),
        ),
      ),
    );
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    // 一块屏不画排布图；看着窗口时屏上不打勾
    expect(find.byKey(const Key('desk-display-map-1')), findsNothing);
    expect(find.byKey(const Key('desk-display-1')), findsOneWidget);
    expect(find.byKey(const Key('desk-windows-loading')), findsOneWidget);
    expect(find.byIcon(Icons.check_rounded), findsNothing);
    final sheetHeight = tester.getSize(find.byType(BottomSheet)).height;
    windows.complete(_windows);
    await tester.pumpAndSettle();
    expect(find.byIcon(Icons.check_rounded), findsOneWidget);
    // 窗口列表到了面板也不长高，免得弹出途中跳一下
    expect(tester.getSize(find.byType(BottomSheet)).height, sheetHeight);
  });

  testWidgets(
    'a lost window dismisses the picker but resolution changes keep it open',
    (tester) async {
      final screen = <String, Object?>{
        ..._frame,
        'displays': [(_frame['displays']! as List).first],
      };
      final target = ValueNotifier(DeskDisplays.parse(screen));
      addTearDown(target.dispose);
      final resolutions = <int>[];
      DeskPick? picked;
      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          locale: const Locale('zh'),
          localizationsDelegates: AppLocalizations.localizationsDelegates,
          supportedLocales: AppLocalizations.supportedLocales,
          home: Builder(
            builder: (context) => Scaffold(
              body: ValueListenableBuilder<DeskDisplays>(
                valueListenable: target,
                builder: (context, state, _) => Stack(
                  children: [
                    Align(
                      alignment: Alignment.topCenter,
                      child: TextButton(
                        onPressed: () async {
                          picked = await pickDeskView(
                            context,
                            target.value,
                            target: target,
                            windows: Future.value(_windows),
                            resolution: 0,
                            onResolution: (value) async {
                              resolutions.add(value);
                            },
                          );
                        },
                        child: const Text('open'),
                      ),
                    ),
                    if (state.gone)
                      Align(
                        alignment: Alignment.bottomCenter,
                        child: Text(context.l10n.deskWindowGone),
                      ),
                  ],
                ),
              ),
            ),
          ),
        ),
      );
      await tester.tap(find.text('open'));
      await tester.pumpAndSettle();
      await tester.ensureVisible(find.byKey(const Key('desk-window-42')));
      await tester.tap(find.byKey(const Key('desk-window-42')));
      await tester.pumpAndSettle();
      expect(picked, (window: true, id: 42));
      // 和页面一样把订阅里的 displays 交给监听器，窗口列表仍是打开时的快照。
      target.value = DeskDisplays.parse({
        ...screen,
        'window': _rawWindows.first,
      });
      await tester.tap(find.text('open'));
      await tester.pumpAndSettle();
      expect(
        find.descendant(
          of: find.byKey(const Key('desk-window-42')),
          matching: find.byIcon(Icons.check_rounded),
        ),
        findsOneWidget,
      );
      await tester.tap(find.text('720p'));
      target.value = DeskDisplays.parse({
        ...screen,
        'window': {..._rawWindows.first, 'w': 800, 'h': 600},
      });
      await tester.pumpAndSettle();
      expect(resolutions, [720]);
      expect(find.byType(BottomSheet), findsOneWidget);
      expect(find.text(zh.deskWindowGone), findsNothing);
      target.value = DeskDisplays.parse({...screen, 'gone': true});
      await tester.pumpAndSettle();
      expect(target.value.window, isNull);
      expect(find.byType(BottomSheet), findsNothing);
      expect(find.byKey(const Key('desk-window-42')), findsNothing);
      expect(find.text(zh.deskWindowGone).hitTestable(), findsOneWidget);
      expect(picked, isNull);
      expect(tester.takeException(), isNull);
    },
  );

  test('windows parse and a lost window is flagged', () {
    final list = DeskWindow.parseList({
      'windows': [
        ..._rawWindows,
        {'id': 0, 'app': 'bad'},
        {'id': 5},
      ],
    });
    expect(list.map((w) => w.id), [42, 9]);
    expect(list.first.label, 'posse — README.md');
    expect(list.last.label, 'Simulator');
    final back = DeskDisplays.parse({..._frame, 'gone': true});
    expect(back.gone, isTrue);
    expect(back.window, isNull);
    expect(DeskDisplays.parse(_frame).gone, isFalse);
  });

  // 横屏的 iPhone：高度只有 390，面板要能滚，不能溢出
  testWidgets('the picker fits a phone held sideways', (tester) async {
    tester.view.physicalSize = const Size(2532, 1170);
    tester.view.devicePixelRatio = 3;
    addTearDown(tester.view.reset);
    final d = DeskDisplays.parse(_frame);
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: Builder(
          builder: (context) => TextButton(
            onPressed: () =>
                pickDeskView(context, d, windows: Future.value(_windows)),
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

  // App 图标按键共用一份：同一个 App 的窗口都画它，没给图标或给坏了的画默认图标
  testWidgets('windows show their app icons', (tester) async {
    // 1×1 的透明 PNG
    const png =
        'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==';
    final list = DeskWindow.parseList({
      'windows': [
        {'id': 1, 'app': 'Code', 'icon': 'com.microsoft.VSCode'},
        {'id': 2, 'app': 'Code', 'icon': 'com.microsoft.VSCode'},
        {'id': 3, 'app': 'Notes', 'icon': 'bad'},
        {'id': 4, 'app': 'Terminal'},
      ],
      'icons': {'com.microsoft.VSCode': png, 'bad': '!!not base64!!'},
    });
    expect(list.map((w) => w.icon != null), [true, true, false, false]);
    expect(identical(list[0].icon, list[1].icon), isTrue);

    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: Builder(
          builder: (context) => TextButton(
            onPressed: () => pickDeskView(
              context,
              DeskDisplays.parse(_frame),
              windows: Future.value(list),
            ),
            child: const Text('open'),
          ),
        ),
      ),
    );
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('desk-window-icon-1')), findsOneWidget);
    expect(find.byKey(const Key('desk-window-icon-2')), findsOneWidget);
    expect(find.byKey(const Key('desk-window-icon-3')), findsNothing);
    expect(
      find.descendant(
        of: find.byKey(const Key('desk-window-4')),
        matching: find.byIcon(Icons.web_asset_rounded),
      ),
      findsOneWidget,
    );
  });
}
