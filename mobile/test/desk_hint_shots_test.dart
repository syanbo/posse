import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/desk/display_picker.dart';
import 'package:posse_mobile/desk/displays.dart';
import 'package:posse_mobile/desk/hint.dart';
import 'package:posse_mobile/desk/unlock.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/ui/theme.dart';

import 'shot_fonts.dart';

// 看桌面手势提示、选画面、解锁弹层的离屏截图（中文、英文各一套），给人看样子用：
// POSSE_SHOTS_DIR=/tmp/shots fvm flutter test test/desk_hint_shots_test.dart
// 字体见 shot_fonts.dart。

const _langs = ['zh', 'en'];

MaterialApp _app(String lang, bool dark, Widget home) => MaterialApp(
  debugShowCheckedModeBanner: false,
  theme: posseTheme(dark ? Brightness.dark : Brightness.light),
  locale: Locale(lang),
  localizationsDelegates: AppLocalizations.localizationsDelegates,
  supportedLocales: AppLocalizations.supportedLocales,
  home: home,
);

// 底下垫一块像桌面的渐变，看遮罩的效果
const _desktop = DecoratedBox(
  decoration: BoxDecoration(
    gradient: LinearGradient(
      begin: Alignment.topLeft,
      end: Alignment.bottomRight,
      colors: [Color(0xFF3A6EA5), Color(0xFFE8B86D)],
    ),
  ),
);

final _displays = DeskDisplays.parse({
  'type': 'displays',
  'current': 1,
  'displays': [
    {
      'id': 1,
      'name': '',
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
  ],
});

Future<void> _shot(WidgetTester tester, GlobalKey key, String path) async {
  await tester.runAsync(() async {
    final boundary =
        key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
    final image = await boundary.toImage(pixelRatio: 2);
    final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
    File(path).writeAsBytesSync(bytes!.buffer.asUint8List());
  });
}

void main() {
  final dir = Platform.environment['POSSE_SHOTS_DIR'] ?? '';

  testWidgets('desk hint screenshots', (tester) async {
    await loadShotFonts(tester);
    await tester.binding.setSurfaceSize(const Size(390, 844));
    for (final lang in _langs) {
      final tail = lang == 'zh' ? '' : '-$lang';
      for (final dark in [false, true]) {
        for (final trackpad in [true, false]) {
          final key = GlobalKey();
          await tester.pumpWidget(
            RepaintBoundary(
              key: key,
              child: _app(
                lang,
                dark,
                Stack(
                  fit: StackFit.expand,
                  children: [
                    _desktop,
                    DeskGestureHint(onDismiss: () {}, trackpad: trackpad),
                  ],
                ),
              ),
            ),
          );
          await tester.pumpAndSettle();
          final name =
              '${trackpad ? 'trackpad' : 'direct'}-${dark ? 'dark' : 'light'}';
          await _shot(tester, key, '$dir/desk-hint-$name$tail.png');
        }
      }
    }
  }, skip: dir.isEmpty);

  testWidgets('desk picker and unlock screenshots', (tester) async {
    await loadShotFonts(tester);
    await tester.binding.setSurfaceSize(const Size(390, 844));
    for (final lang in _langs) {
      final tail = lang == 'zh' ? '' : '-$lang';
      for (final dark in [false, true]) {
        final mode = dark ? 'dark' : 'light';
        final key = GlobalKey();
        await tester.pumpWidget(
          RepaintBoundary(
            key: key,
            child: _app(
              lang,
              dark,
              Builder(
                builder: (context) => Stack(
                  fit: StackFit.expand,
                  children: [
                    _desktop,
                    Center(
                      child: Column(
                        mainAxisSize: MainAxisSize.min,
                        children: [
                          TextButton(
                            onPressed: () => pickDeskView(
                              context,
                              _displays,
                              windows: Future.value(const []),
                            ),
                            child: const Text('picker'),
                          ),
                          TextButton(
                            onPressed: () =>
                                askMacPassword(context, saved: true),
                            child: const Text('unlock'),
                          ),
                        ],
                      ),
                    ),
                  ],
                ),
              ),
            ),
          ),
        );
        await tester.tap(find.text('picker'));
        await tester.pumpAndSettle();
        await _shot(tester, key, '$dir/desk-picker-$mode$tail.png');
        await tester.tapAt(const Offset(20, 20));
        await tester.pumpAndSettle();
        await tester.tap(find.text('unlock'));
        await tester.pumpAndSettle();
        await _shot(tester, key, '$dir/desk-unlock-$mode$tail.png');
        await tester.tapAt(const Offset(20, 20));
        await tester.pumpAndSettle();
      }
    }
  }, skip: dir.isEmpty);
}
