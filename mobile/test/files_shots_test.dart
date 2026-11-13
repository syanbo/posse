import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/files/files.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/ui/theme.dart';

import 'shot_fonts.dart';

// 文件树按类型的图标的离屏截图，给人看样子用：
// POSSE_SHOTS_DIR=/tmp/shots fvm flutter test test/files_shots_test.dart

const _names = [
  'ios/Runner.xcodeproj/',
  'ios/Assets.xcassets/',
  'lib/',
  'shared@/',
  'CLAUDE.md@',
  'main.go',
  'app.ts',
  'index.js',
  'main.dart',
  'pubspec.yaml',
  'AppDelegate.swift',
  'MainActivity.kt',
  'App.java',
  'App.vue',
  'build.gradle.kts',
  'AndroidManifest.xml',
  'Dockerfile',
  'README.md',
  'styles.css',
  'index.html',
  'config.json',
  'build.sh',
  'logo.png',
  'demo.mp4',
  'data.csv',
  'schema.sql',
  'dist.zip',
  'docs.pdf',
  'go.sum',
  '.gitignore',
  'upload.jks',
  'notes.txt',
];

String _clean(String n) {
  var s = n.replaceAll('@', '');
  if (s.endsWith('/')) s = s.substring(0, s.length - 1);
  return s.substring(s.lastIndexOf('/') + 1);
}

class _Rpc implements PhoneRpc {
  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async => {
    'directories': {
      '': {
        'entries': [
          for (final n in _names)
            {
              'name': _clean(n),
              'isDirectory': n.endsWith('/'),
              // 名字带 @ 的是符号链接
              'isSymlink': n.contains('@'),
              'gitStatus': switch (_clean(n)) {
                'main.go' || 'lib' => 'M',
                'app.ts' => 'A',
                'index.js' => '?',
                'main.dart' => 'U',
                _ => '',
              },
              'ignored': _clean(n) == 'dist.zip',
            },
        ],
      },
    },
  };

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class _Model extends PosseAppModel {
  _Model()
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());

  final _rpc = _Rpc();

  @override
  PhoneRpc? get rpc => _rpc;
}

void main() {
  final dir = Platform.environment['POSSE_SHOTS_DIR'] ?? '';

  testWidgets('file tree screenshots', (tester) async {
    await loadShotFonts(tester);
    await tester.binding.setSurfaceSize(const Size(390, 1300));
    for (final dark in [false, true]) {
      final key = GlobalKey();
      await tester.pumpWidget(
        RepaintBoundary(
          key: key,
          child: MaterialApp(
            debugShowCheckedModeBanner: false,
            theme: posseTheme(dark ? Brightness.dark : Brightness.light),
            home: Scaffold(
              body: FilesPane(model: _Model(), worktree: 'w'),
            ),
          ),
        ),
      );
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 400));
      expect(find.text('main.go'), findsOneWidget);
      if (dir.isEmpty) continue;
      await tester.runAsync(() async {
        final boundary =
            key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
        final image = await boundary.toImage(pixelRatio: 2);
        final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
        File('$dir/files-${dark ? 'dark' : 'light'}.png')
            .writeAsBytesSync(bytes!.buffer.asUint8List());
      });
    }
    await tester.pumpWidget(const SizedBox());
  });
}
