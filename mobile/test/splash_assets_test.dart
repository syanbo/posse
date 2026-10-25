import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/ui/splash.dart';

// 原生闪屏的标志图和整屏预览，都由 lib/ui/splash.dart 画出来：
//   POSSE_SPLASH_EXPORT=1 fvm flutter test test/splash_assets_test.dart   写原生闪屏的标志、slogan 和 Flutter 用的 slogan 图
//   POSSE_SHOTS_DIR=/tmp/shots fvm flutter test test/splash_assets_test.dart   出整屏预览
// 不设环境变量时只检查闪屏能建出来、按时退场。

Future<void> _save(
  WidgetTester tester,
  GlobalKey key,
  double ratio,
  String path,
) => tester.runAsync(() async {
  final boundary =
      key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
  final image = await boundary.toImage(pixelRatio: ratio);
  final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
  File(path)
    ..parent.createSync(recursive: true)
    ..writeAsBytesSync(bytes!.buffer.asUint8List());
});

void main() {
  final export = Platform.environment['POSSE_SPLASH_EXPORT'] == '1';
  final shots = Platform.environment['POSSE_SHOTS_DIR'] ?? '';

  const androidDensities = [
    ('mdpi', 1.0),
    ('hdpi', 1.5),
    ('xhdpi', 2.0),
    ('xxhdpi', 3.0),
    ('xxxhdpi', 4.0),
  ];
  const res = 'android/app/src/main/res';

  testWidgets('export native splash mark', (tester) async {
    final key = GlobalKey();
    await tester.pumpWidget(
      Center(
        child: RepaintBoundary(
          key: key,
          child: const CustomPaint(
            size: Size.square(splashCanvas),
            painter: PosseMarkPainter(),
          ),
        ),
      ),
    );
    if (!export) return;
    const ios = 'ios/Runner/Assets.xcassets/LaunchImage.imageset';
    await _save(tester, key, 1, '$ios/LaunchImage.png');
    await _save(tester, key, 2, '$ios/LaunchImage@2x.png');
    await _save(tester, key, 3, '$ios/LaunchImage@3x.png');
    for (final (dir, ratio) in androidDensities) {
      await _save(tester, key, ratio, '$res/drawable-$dir/splash_mark.png');
    }
  });

  testWidgets('export splash slogan', (tester) async {
    if (!export) return;
    // 测试里没有真字体，借 macOS 自带的 Avenir（集合里第一个是 Book 字重）把字画出来
    await tester.runAsync(() async {
      final font = File('/System/Library/Fonts/Avenir.ttc').readAsBytes();
      await (FontLoader(
        'SplashFont',
      )..addFont(font.then((b) => ByteData.sublistView(b)))).load();
    });
    final key = GlobalKey();
    await tester.pumpWidget(
      Center(
        child: RepaintBoundary(
          key: key,
          child: const SplashSlogan(fontFamily: 'SplashFont'),
        ),
      ),
    );
    await tester.pump();
    const ios = 'ios/Runner/Assets.xcassets/SplashSlogan.imageset';
    await _save(tester, key, 1, '$ios/SplashSlogan.png');
    await _save(tester, key, 2, '$ios/SplashSlogan@2x.png');
    await _save(tester, key, 3, '$ios/SplashSlogan@3x.png');
    for (final (dir, ratio) in androidDensities) {
      await _save(tester, key, ratio, '$res/drawable-$dir/splash_slogan.png');
    }
    await _save(tester, key, 1, 'assets/splash_slogan.png');
    await _save(tester, key, 2, 'assets/2.0x/splash_slogan.png');
    await _save(tester, key, 3, 'assets/3.0x/splash_slogan.png');
  });

  testWidgets('splash plays and leaves', (tester) async {
    tester.view.physicalSize = const Size(1179, 2556);
    tester.view.devicePixelRatio = 3;
    tester.view.padding = const FakeViewPadding(top: 162, bottom: 102);
    addTearDown(tester.view.reset);
    final key = GlobalKey();
    var done = false;
    await tester.pumpWidget(
      MaterialApp(
        home: RepaintBoundary(
          key: key,
          child: PosseSplash(ready: true, onDone: () => done = true),
        ),
      ),
    );
    await tester.pump(const Duration(milliseconds: 650));
    if (shots.isNotEmpty) {
      await _save(tester, key, 1, '$shots/splash.png');
    }
    expect(done, isFalse);
    await tester.pump(const Duration(milliseconds: 100));
    await tester.pumpAndSettle();
    expect(done, isTrue);
  });
}
