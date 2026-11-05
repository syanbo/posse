import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/git/branches.dart';
import 'package:posse_mobile/git/git.dart';
import 'package:posse_mobile/git/status.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/ui/theme.dart';

import 'shot_fonts.dart';

// 改动页的分支卡和切换分支面板的离屏截图，给人看样子用：
// POSSE_SHOTS_DIR=/tmp/shots fvm flutter test test/branches_shots_test.dart
// 字体见 shot_fonts.dart。

void main() {
  final dir = Platform.environment['POSSE_SHOTS_DIR'] ?? '';

  testWidgets('branch sheet screenshots', (tester) async {
    await loadShotFonts(tester);
    await tester.binding.setSurfaceSize(const Size(390, 844));
    final branches = parseGitBranches({
      'head': 'main',
      'branches': [
        {
          'name': 'main',
          'current': true,
          'time': 100,
          'upstream': 'origin/main',
          'ahead': 2,
        },
        {
          'name': 'feat/mobile-branch',
          'time': 90,
          'upstream': 'origin/feat/mobile-branch',
          'behind': 1,
        },
        {
          'name': 'skills',
          'time': 80,
          'elsewhere': '/x/worktrees/posse/skills',
          'workspace': 'p1::w2',
        },
        {'name': 'review', 'time': 70, 'elsewhere': '/x/posse-review'},
        {'name': 'old-ui', 'time': 10},
        {'name': 'origin/main', 'remote': true, 'time': 100},
        {'name': 'origin/release/0.1.25', 'remote': true, 'time': 95},
        {'name': 'origin/fix/relay', 'remote': true, 'time': 60},
      ],
    });
    for (final (lang, dark) in [('zh', false), ('zh', true), ('en', false)]) {
      final key = GlobalKey();
      await tester.pumpWidget(
        RepaintBoundary(
          key: key,
          child: MaterialApp(
            debugShowCheckedModeBanner: false,
            theme: posseTheme(dark ? Brightness.dark : Brightness.light),
            locale: Locale(lang),
            localizationsDelegates: AppLocalizations.localizationsDelegates,
            supportedLocales: AppLocalizations.supportedLocales,
            home: Scaffold(
              body: GitPane(
                title: 'posse',
                loadStatus: () async => parseGitStatus({
                  'branch': 'main',
                  'upstream': 'origin/main',
                  'ahead': 2,
                  'files': [
                    {
                      'path': 'lib/git/git.dart',
                      'x': '.',
                      'y': 'M',
                      'added': 12,
                      'deleted': 3,
                    },
                  ],
                }),
                loadDiff: (file) async => parseGitDiff({'path': file.path}),
                branches: GitBranchActions(
                  load: (repo) async => branches,
                  checkout: (repo, b, force) async =>
                      const GitCheckoutResult(switched: false),
                  workspaceName: (id) => 'posse / skills',
                  openWorkspace: (_) {},
                ),
              ),
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();
      Future<void> shot(String name) async {
        if (dir.isEmpty) return;
        await tester.runAsync(() async {
          final boundary =
              key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
          final image = await boundary.toImage(pixelRatio: 3);
          final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
          File(
            '$dir/branches-$name-${dark ? 'dark' : 'light'}${lang == 'zh' ? '' : '-$lang'}.png',
          ).writeAsBytesSync(bytes!.buffer.asUint8List());
        });
      }

      await shot('card');
      await tester.tap(find.text('main').first);
      await tester.pumpAndSettle();
      expect(find.text('old-ui'), findsOneWidget);
      await shot('sheet');
    }
  });
}
