import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/workspace/models.dart';
import 'package:posse_mobile/workspace/workspace.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'shot_fonts.dart';

// 首页项目带 worktree 时的离屏截图，给人看样子用：
// POSSE_SHOTS_DIR=/tmp/shots fvm flutter test test/workspace_shots_test.dart
// 字体见 shot_fonts.dart。

void main() {
  final dir = Platform.environment['POSSE_SHOTS_DIR'] ?? '';

  testWidgets('workspace screenshots', (tester) async {
    await loadShotFonts(tester);
    SharedPreferences.setMockInitialValues({});
    await tester.binding.setSurfaceSize(const Size(390, 844));
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    model.worktrees = parseWorktrees([
      {
        'worktreeId': 'p1::w1',
        'repo': 'posse',
        'displayName': 'posse',
        'path': '/Users/x/posse',
        'branch': 'main',
        'isMainWorktree': true,
        'liveTerminalCount': 3,
      },
      {
        'worktreeId': 'p1::w2',
        'repo': 'posse',
        'displayName': 'skills',
        'path': '/Users/x/.posse/worktrees/posse/skills',
        'branch': 'skills',
        'isWorktree': true,
        'liveTerminalCount': 1,
      },
      {
        'worktreeId': 'p1::w3',
        'repo': 'posse',
        'displayName': 'notes',
        'path': '/Users/x/posse',
        'isWorktree': false,
      },
    ]);
    model.tabs = {
      'p1::w1': const [
        SessionTab(
          worktreeId: 'p1::w1',
          title: '界面持续跳动问题',
          terminalId: 't1',
          agent: 'claude',
          state: 'done',
        ),
        SessionTab(
          worktreeId: 'p1::w1',
          title: 'Posse 交接任务阻塞与 Agent 汇报',
          terminalId: 't2',
        ),
      ],
      'p1::w2': const [
        SessionTab(
          worktreeId: 'p1::w2',
          title: '技能管理方案',
          terminalId: 't3',
          agent: 'cursor',
          state: 'working',
        ),
        SessionTab(
          worktreeId: 'p1::w2',
          title: 'Rename the key bar',
          terminalId: 't4',
          agent: 'codex',
          state: 'waiting',
          toolName: 'Bash',
        ),
      ],
    };
    for (final (lang, dark, running) in [
      ('zh', false, false),
      ('zh', true, false),
      ('zh', false, true),
      ('zh', true, true),
      ('en', false, false),
      ('en', false, true),
    ]) {
      model.settings.homeByStatus = running;
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
            home: WorkspaceScreen(model: model),
          ),
        ),
      );
      await tester.pump(const Duration(milliseconds: 400));
      if (!running && lang == 'zh') {
        expect(find.text('主工作区'), findsOneWidget);
      }
      if (dir.isEmpty) continue;
      await tester.runAsync(() async {
        final boundary =
            key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
        final image = await boundary.toImage(pixelRatio: 3);
        final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
        File(
          '$dir/workspace-${running ? 'running-' : ''}${dark ? 'dark' : 'light'}${lang == 'zh' ? '' : '-$lang'}.png',
        ).writeAsBytesSync(bytes!.buffer.asUint8List());
      });
    }
  });
}
