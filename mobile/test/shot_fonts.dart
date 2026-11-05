import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/ui/theme.dart';

// 离屏截图用的字体。flutter_test 只带一个把字画成方块的 FlutterTest 字体，没装的字族都落到它：
// 正文的 Roboto 和兜底的 FlutterTest 换成苹方（和 iPhone 一样），等宽的 Menlo 用系统自带的，
// 终端提示符的图标用 App 自带的 Nerd Font。苹方在 macOS 上是按需下载的资源，找不到时用冬青黑体。
Future<void> loadShotFonts(WidgetTester tester) async {
  final root = Platform.environment['FLUTTER_ROOT']!;
  final cjk = [
    ..._pingFang(),
    '/System/Library/Fonts/Hiragino Sans GB.ttc',
    '/Library/Fonts/Arial Unicode.ttf',
  ].where((p) => File(p).existsSync()).firstOrNull;
  final fonts = {
    if (cjk != null) ...{'Roboto': cjk, 'FlutterTest': cjk, 'PingFang SC': cjk},
    'Menlo': '/System/Library/Fonts/Menlo.ttc',
    'Symbols Nerd Font Mono': 'assets/fonts/SymbolsNerdFontMono-Regular.ttf',
    'MaterialIcons':
        '$root/bin/cache/artifacts/material_fonts/MaterialIcons-Regular.otf',
  };
  // 不是 macOS（找不到的字体跳过）时照常跑，只是截图里的字是方块
  await tester.runAsync(() async {
    for (final e in fonts.entries) {
      if (!File(e.value).existsSync()) continue;
      final bytes = File(e.value).readAsBytesSync();
      await (FontLoader(
        e.key,
      )..addFont(Future.value(ByteData.sublistView(bytes)))).load();
    }
  });
}

/// 苹方是 macOS 按需下载的字体资源，路径带哈希；目录读不了时当没有。
Iterable<String> _pingFang() sync* {
  try {
    for (final d in Directory('/System/Library/AssetsV2').listSync()) {
      if (!d.path.contains('com_apple_MobileAsset_Font')) continue;
      for (final f in Directory(d.path).listSync(recursive: true)) {
        if (f.path.endsWith('/PingFang.ttc')) yield f.path;
      }
    }
  } on FileSystemException {
    return;
  }
}

/// 截图用的主题：按钮的字样式不写字体，测试里落到画方块的默认字体（真机落到系统字体），这里补上。
ThemeData shotTheme(ThemeData base) {
  const label = WidgetStatePropertyAll(
    TextStyle(fontFamily: 'Roboto', fontSize: Fs.body, fontWeight: Fw.medium),
  );
  return base.copyWith(
    filledButtonTheme: FilledButtonThemeData(
      style: base.filledButtonTheme.style!.copyWith(textStyle: label),
    ),
    textButtonTheme: TextButtonThemeData(
      style: base.textButtonTheme.style!.copyWith(textStyle: label),
    ),
  );
}
