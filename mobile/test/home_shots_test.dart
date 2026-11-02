import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/settings/custom_keys_screen.dart';
import 'package:posse_mobile/settings/settings.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/update/update.dart';
import 'package:posse_mobile/workspace/models.dart';
import 'package:posse_mobile/workspace/new_session.dart';
import 'package:posse_mobile/workspace/session_actions.dart';
import 'package:posse_mobile/workspace/workspace.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'shot_fonts.dart';

// 配对页、设置、快捷键、新建会话、会话的更多、关闭确认和更新说明的离屏截图，中文和英文各一套，给人看排版：
// POSSE_SHOTS_DIR=/tmp/shots fvm flutter test test/home_shots_test.dart
// 字体见 shot_fonts.dart。看桌面一节只在手机上显示，这里截不到。

PosseAppModel _model() {
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
      'liveTerminalCount': 1,
    },
  ]);
  return model;
}

/// 主题里顶栏标题和按钮的字没写字族，测试里落到画方块的 FlutterTest 字体（手机上是系统字体）：截图时补上 Roboto，
/// 由 shot_fonts.dart 换成苹方。
ThemeData _shotTheme() {
  final t = posseTheme(Brightness.light);
  const font = TextStyle(fontFamily: 'Roboto');
  ButtonStyle? fix(ButtonStyle? s) => s?.copyWith(
    textStyle: WidgetStatePropertyAll(
      (s.textStyle?.resolve({}) ?? const TextStyle()).merge(font),
    ),
  );
  return t.copyWith(
    appBarTheme: t.appBarTheme.copyWith(
      titleTextStyle: t.appBarTheme.titleTextStyle?.merge(font),
    ),
    filledButtonTheme: FilledButtonThemeData(
      style: fix(t.filledButtonTheme.style),
    ),
    textButtonTheme: TextButtonThemeData(style: fix(t.textButtonTheme.style)),
    outlinedButtonTheme: OutlinedButtonThemeData(
      style:
          fix(t.outlinedButtonTheme.style) ??
          const ButtonStyle(textStyle: WidgetStatePropertyAll(font)),
    ),
  );
}

const _tab = SessionTab(
  worktreeId: 'p1::w1',
  title: 'i18n',
  terminalId: 't1',
  agent: 'claude',
  state: 'working',
);

void main() {
  final dir = Platform.environment['POSSE_SHOTS_DIR'] ?? '';

  for (final lang in ['zh', 'en']) {
    testWidgets('home screenshots ($lang)', (tester) async {
      await loadShotFonts(tester);
      SharedPreferences.setMockInitialValues({});
      updates.latest = const AppRelease(
        version: '1.4.0',
        build: 512,
        size: '14.5 MB',
        notes: ['…'],
        install: '',
      );
      addTearDown(() => updates.latest = null);

      Future<void> shot(
        String name,
        Widget home, {
        double height = 844,
        Future<void> Function(BuildContext context)? open,
      }) async {
        await tester.binding.setSurfaceSize(Size(390, height));
        final key = GlobalKey();
        final ctx = GlobalKey();
        await tester.pumpWidget(
          RepaintBoundary(
            key: key,
            child: MaterialApp(
              debugShowCheckedModeBanner: false,
              theme: _shotTheme(),
              locale: Locale(lang),
              localizationsDelegates: AppLocalizations.localizationsDelegates,
              supportedLocales: AppLocalizations.supportedLocales,
              home: KeyedSubtree(key: ctx, child: home),
            ),
          ),
        );
        await tester.pump(const Duration(milliseconds: 400));
        if (open != null) {
          final context = ctx.currentContext!;
          // 不等弹出的结果：截完图就换下一张
          open(context).ignore();
          await tester.pump();
          await tester.pump(const Duration(milliseconds: 600));
        }
        expect(tester.takeException(), isNull);
        if (dir.isEmpty) return;
        await tester.runAsync(() async {
          final boundary =
              key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
          final image = await boundary.toImage(pixelRatio: 2);
          final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
          File('$dir/$name-$lang.png')
              .writeAsBytesSync(bytes!.buffer.asUint8List());
        });
      }

      final model = _model();
      await shot('pair', PairScreen(model: model, manual: true, note: 'x'));
      await shot('pair-add', PairScreen(model: model, adding: true));
      await shot(
        'settings',
        SettingsScreen(model: model, onComputers: () {}),
        height: 1700,
      );
      await shot('keys', CustomKeysScreen(model: model), height: 1300);
      await shot(
        'new-session',
        const Scaffold(),
        open: (context) => showNewSession(context, model),
      );
      await shot(
        'session-more',
        const Scaffold(),
        open: (context) => showSessionMore(
          context,
          tab: _tab,
          switchLabel: context.l10n.terminalSwitchToChat,
          desk: false,
          media: true,
          tools: true,
        ),
      );
      await shot(
        'close-session',
        const Scaffold(),
        open: (context) => closeSession(context, model, _tab),
      );
      await shot(
        'update',
        const Scaffold(),
        open: (context) => showUpdateSheet(context, updates.latest!),
      );
      await tester.binding.setSurfaceSize(null);
    });
  }
}
