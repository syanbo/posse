import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/desk/hint.dart';
import 'package:posse_mobile/ui/theme.dart';

// 看桌面手势提示的离屏截图，给人看样子用：
// POSSE_SHOTS_DIR=/tmp/shots fvm flutter test test/desk_hint_shots_test.dart
// 字体的顶替办法同 usage_shots_test.dart。

void main() {
  final dir = Platform.environment['POSSE_SHOTS_DIR'] ?? '';

  testWidgets('desk hint screenshots', (tester) async {
    final root = Platform.environment['FLUTTER_ROOT']!;
    final fonts = {
      'Roboto': '/Library/Fonts/Arial Unicode.ttf',
      'MaterialIcons':
          '$root/bin/cache/artifacts/material_fonts/MaterialIcons-Regular.otf',
    };
    await tester.runAsync(() async {
      for (final e in fonts.entries) {
        final bytes = File(e.value).readAsBytesSync();
        await (FontLoader(
          e.key,
        )..addFont(Future.value(ByteData.sublistView(bytes)))).load();
      }
    });
    await tester.binding.setSurfaceSize(const Size(390, 844));
    for (final dark in [false, true]) {
      for (final trackpad in [true, false]) {
        final key = GlobalKey();
        await tester.pumpWidget(
          RepaintBoundary(
            key: key,
            child: MaterialApp(
              debugShowCheckedModeBanner: false,
              theme: posseTheme(dark ? Brightness.dark : Brightness.light),
              // 底下垫一块像桌面的渐变，看遮罩的效果
              home: Stack(
                fit: StackFit.expand,
                children: [
                  const DecoratedBox(
                    decoration: BoxDecoration(
                      gradient: LinearGradient(
                        begin: Alignment.topLeft,
                        end: Alignment.bottomRight,
                        colors: [Color(0xFF3A6EA5), Color(0xFFE8B86D)],
                      ),
                    ),
                  ),
                  DeskGestureHint(onDismiss: () {}, trackpad: trackpad),
                ],
              ),
            ),
          ),
        );
        await tester.pumpAndSettle();
        final name =
            '${trackpad ? 'trackpad' : 'direct'}-${dark ? 'dark' : 'light'}';
        await tester.runAsync(() async {
          final boundary =
              key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
          final image = await boundary.toImage(pixelRatio: 2);
          final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
          File('$dir/desk-hint-$name.png')
              .writeAsBytesSync(bytes!.buffer.asUint8List());
        });
      }
    }
  }, skip: dir.isEmpty);
}
