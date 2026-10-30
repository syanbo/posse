import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/chat/markdown_view.dart';
import 'package:posse_mobile/ui/mermaid.dart';
import 'package:posse_mobile/ui/theme.dart';

// 经真的 Go 库排版（flutter test 的构建钩子把它编成本机的动态库）
const _flow =
    'flowchart LR\n  UI[窗口] -- attach --> D[常驻进程]\n  D --> T{还活着?}\n  T -- 是 --> K[保留副本]';
const _seq =
    'sequenceDiagram\n  participant U as 窗口\n  participant D as 常驻进程\n  U->>D: attach\n  D-->>U: 快照';

Widget _app(String md, {bool dark = false}) => MaterialApp(
  debugShowCheckedModeBanner: false,
  theme: posseTheme(dark ? Brightness.dark : Brightness.light),
  home: Scaffold(
    body: SingleChildScrollView(
      padding: const EdgeInsets.all(16),
      child: Builder(
        builder: (context) => MarkdownView(
          md,
          style: TextStyle(
            fontSize: Fs.read,
            height: 1.6,
            color: PosseColors.of(context).text,
          ),
        ),
      ),
    ),
  ),
);

/// 等后台排版回来。
Future<void> _settle(WidgetTester tester, Finder until, [int count = 1]) async {
  for (var i = 0; i < 100 && until.evaluate().length < count; i++) {
    await tester.runAsync(
      () => Future<void>.delayed(const Duration(milliseconds: 20)),
    );
    await tester.pump();
  }
}

void main() {
  setUp(resetMermaidCache);

  testWidgets('a mermaid block draws as a diagram and toggles to source', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 900));
    await tester.pumpWidget(_app('```mermaid\n$_flow\n```'));
    // 画好之前照代码块显示
    expect(find.textContaining('常驻进程', findRichText: true), findsOneWidget);
    await _settle(tester, find.byType(MermaidDiagram));
    expect(find.byType(MermaidDiagram), findsOneWidget);
    // 比屏幕宽的图等比缩小到可用宽度以内
    expect(tester.getSize(find.byType(MermaidDiagram)).width, lessThan(390));

    await tester.tap(find.byIcon(Icons.code));
    await tester.pump();
    expect(find.byType(MermaidDiagram), findsNothing);
    expect(find.textContaining('常驻进程', findRichText: true), findsOneWidget);
    await tester.tap(find.byIcon(Icons.visibility_outlined));
    await tester.pump();
    expect(find.byType(MermaidDiagram), findsOneWidget);
  });

  testWidgets('a broken diagram shows the error over its source', (
    tester,
  ) async {
    await tester.pumpWidget(
      _app('```mermaid\nsequenceDiagram\n  A->>B hi\n```'),
    );
    final err = find.textContaining('画不出图');
    await _settle(tester, err);
    expect(err, findsOneWidget);
    expect(find.textContaining('line 2'), findsOneWidget);
    expect(find.textContaining('A->>B hi', findRichText: true), findsOneWidget);
  });

  testWidgets('a failed render shows its error instead of only the source', (
    tester,
  ) async {
    final real = mermaidRender;
    mermaidRender = (_) => Future.error(StateError('库加载不了'));
    addTearDown(() => mermaidRender = real);
    await tester.pumpWidget(_app('```mermaid\n$_flow\n```'));
    final err = find.textContaining('画不出图');
    await _settle(tester, err);
    expect(find.textContaining('库加载不了'), findsOneWidget);
    expect(find.textContaining('常驻进程', findRichText: true), findsOneWidget);
  });

  // 离屏截图给人看：POSSE_SHOTS_DIR=/tmp/shots fvm flutter test test/mermaid_test.dart
  final dir = Platform.environment['POSSE_SHOTS_DIR'] ?? '';
  testWidgets('mermaid screenshots', (tester) async {
    final root = Platform.environment['FLUTTER_ROOT']!;
    final fonts = {
      'Roboto': '/Library/Fonts/Arial Unicode.ttf',
      'Menlo': '/System/Library/Fonts/Menlo.ttc',
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
    await tester.binding.setSurfaceSize(const Size(390, 900));
    for (final dark in [false, true]) {
      final key = GlobalKey();
      await tester.pumpWidget(
        RepaintBoundary(
          key: key,
          child: _app(
            '## 结构\n\n```mermaid\n$_flow\n```\n\n## 接入\n\n```mermaid\n$_seq\n```',
            dark: dark,
          ),
        ),
      );
      await _settle(tester, find.byType(MermaidDiagram), 2);
      await tester.pump();
      await tester.runAsync(() async {
        final boundary =
            key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
        final image = await boundary.toImage(pixelRatio: 2);
        final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
        File('$dir/mermaid-${dark ? 'dark' : 'light'}.png')
            .writeAsBytesSync(bytes!.buffer.asUint8List());
      });
    }
  }, skip: dir.isEmpty);
}
