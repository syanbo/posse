import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/ui/line_icons.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/ui/widgets.dart';
import 'package:posse_mobile/usage/usage.dart';

import 'shot_fonts.dart';

// 用量卡片和详情的离屏截图，给人看样子用：
// POSSE_SHOTS_DIR=/tmp/shots fvm flutter test test/usage_shots_test.dart
// 字体见 shot_fonts.dart。

List<AgentUsage> _sample(DateTime now) => [
  AgentUsage(
    agent: 'claude',
    updatedAt: now.subtract(const Duration(minutes: 2)),
    windows: [
      UsageWindow(
        percent: 86,
        minutes: 300,
        resetsAt: now.add(const Duration(minutes: 95)),
      ),
      UsageWindow(
        percent: 41,
        minutes: 10080,
        resetsAt: now.add(const Duration(hours: 73)),
      ),
      UsageWindow(
        percent: 12,
        minutes: 10080,
        resetsAt: now.add(const Duration(hours: 73)),
        name: 'Fable',
        at: now.subtract(const Duration(hours: 3)),
      ),
    ],
    shares: const [('Claude Code', 92), ('Chats', 8)],
    sharesAt: now.subtract(const Duration(hours: 3)),
  ),
  AgentUsage(
    agent: 'codex',
    plan: 'plus',
    updatedAt: now.subtract(const Duration(minutes: 25)),
    windows: [
      UsageWindow(
        percent: 7,
        minutes: 300,
        resetsAt: now.add(const Duration(hours: 4)),
      ),
      UsageWindow(
        percent: 18,
        minutes: 10080,
        resetsAt: now.add(const Duration(hours: 120)),
      ),
    ],
    resets: 3,
    resetsExpireAt: now.add(const Duration(hours: 80)),
  ),
  AgentUsage(
    agent: 'grok',
    plan: 'SuperGrok Heavy',
    updatedAt: now.subtract(const Duration(hours: 3)),
    windows: [
      UsageWindow(
        percent: 53,
        minutes: 10080,
        resetsAt: now.add(const Duration(hours: 90)),
      ),
    ],
  ),
  AgentUsage(
    agent: 'cursor',
    plan: 'Ultra \$200/mo',
    updatedAt: now.subtract(const Duration(minutes: 40)),
    windows: [
      UsageWindow(
        percent: 10.3,
        minutes: 44640,
        resetsAt: now.add(const Duration(days: 20)),
      ),
      UsageWindow(
        percent: 2.5,
        minutes: 44640,
        resetsAt: now.add(const Duration(days: 20)),
        name: 'Auto',
      ),
      UsageWindow(
        percent: 18.2,
        minutes: 44640,
        resetsAt: now.add(const Duration(days: 20)),
        name: 'API',
      ),
    ],
    spend: const UsageMoney(used: 4110, limit: 40000),
    onDemand: const UsageMoney(used: 250, limit: 2000),
  ),
];

/// 首页的样子：顶栏、用量卡片，下面一个项目。
class _Home extends StatelessWidget {
  const _Home({required this.usages});

  final List<AgentUsage> usages;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    Widget session(String agent, String title) => Padding(
      padding: const EdgeInsets.fromLTRB(16, 12, 16, 12),
      child: Row(
        children: [
          AgentTile(agent: agent, size: Ic.button),
          const SizedBox(width: 12),
          Text(
            title,
            style: TextStyle(fontSize: Fs.body, color: c.text),
          ),
        ],
      ),
    );
    return Scaffold(
      appBar: AppBar(
        toolbarHeight: 60,
        titleSpacing: 20,
        title: Text(
          'Posse',
          style: TextStyle(
            fontFamily: 'Roboto',
            fontSize: Fs.title,
            fontWeight: Fw.bold,
            color: c.text,
          ),
        ),
        actions: [
          Icon(Icons.history_rounded, size: Ic.button, color: c.text),
          const SizedBox(width: 16),
          Icon(
            Icons.add_circle_outline_rounded,
            size: Ic.button,
            color: c.text,
          ),
          const SizedBox(width: 16),
          LineIconView(LineIcon.settings, size: Ic.button, color: c.text),
          const SizedBox(width: 20),
        ],
      ),
      body: ListView(
        padding: const EdgeInsets.only(top: 8),
        children: [
          UsageCardView(usages: usages),
          Section(
            inset: 48,
            margin: const EdgeInsets.fromLTRB(10, 0, 10, 10),
            children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(16, 14, 16, 14),
                child: Row(
                  children: [
                    LineIconView(
                      LineIcon.folder,
                      size: Ic.button,
                      color: c.accent,
                    ),
                    const SizedBox(width: 12),
                    Text(
                      'posse',
                      style: TextStyle(
                        fontSize: Fs.body,
                        fontWeight: FontWeight.w500,
                        color: c.text,
                      ),
                    ),
                  ],
                ),
              ),
              session('claude', '显示模型用量'),
              session('codex', '核对 Codex 限额字段'),
            ],
          ),
        ],
      ),
    );
  }
}

Future<void> _shot(WidgetTester tester, GlobalKey key, String path) async {
  await tester.runAsync(() async {
    final boundary =
        key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
    final image = await boundary.toImage(pixelRatio: 3);
    final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
    File(path).writeAsBytesSync(bytes!.buffer.asUint8List());
  });
}

void main() {
  final dir = Platform.environment['POSSE_SHOTS_DIR'] ?? '';

  testWidgets('expired Codex screenshots', (tester) async {
    await loadShotFonts(tester);
    await tester.binding.setSurfaceSize(const Size(390, 844));
    final now = DateTime.now();
    for (final lang in ['zh', 'en']) {
      for (final dark in [false, true]) {
        for (final state in ['off', 'login', 'details']) {
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
                  body: SafeArea(
                    child: UsageSheet(
                      usages: [
                        AgentUsage(
                          agent: 'codex',
                          plan: 'Plus',
                          windows: [
                            UsageWindow(
                              percent: 96,
                              minutes: 300,
                              resetsAt: now.subtract(
                                const Duration(minutes: 2),
                              ),
                            ),
                            UsageWindow(
                              percent: 32,
                              minutes: 10080,
                              resetsAt: now.add(const Duration(days: 2)),
                            ),
                          ],
                          updatedAt: now.subtract(const Duration(minutes: 8)),
                        ),
                      ],
                      codexOff: state == 'off',
                      codexError: switch (state) {
                        'login' =>
                          'codex: login expired; run codex login again',
                        'details' => 'codex: could not read reset credits: context deadline exceeded',
                        _ => '',
                      },
                    ),
                  ),
                ),
              ),
            ),
          );
          await tester.pumpAndSettle();
          expect(tester.takeException(), isNull);
          await _shot(
            tester,
            key,
            '$dir/mobile-codex-$state-$lang-${dark ? 'dark' : 'light'}.png',
          );
        }
      }
    }
  }, skip: dir.isEmpty);

  testWidgets('usage screenshots', (tester) async {
    await loadShotFonts(tester);
    await tester.binding.setSurfaceSize(const Size(390, 844));
    final now = DateTime.now();
    for (final lang in ['zh', 'en']) {
      for (final dark in [false, true]) {
        final key = GlobalKey();
        final theme = posseTheme(dark ? Brightness.dark : Brightness.light);
        await tester.pumpWidget(
          RepaintBoundary(
            key: key,
            child: MaterialApp(
              debugShowCheckedModeBanner: false,
              theme: theme,
              locale: Locale(lang),
              localizationsDelegates: AppLocalizations.localizationsDelegates,
              supportedLocales: AppLocalizations.supportedLocales,
              home: _Home(usages: _sample(now)),
            ),
          ),
        );
        await tester.pumpAndSettle();
        final suffix =
            '${dark ? 'dark' : 'light'}${lang == 'zh' ? '' : '-$lang'}';
        await _shot(tester, key, '$dir/mobile-home-$suffix.png');
        await tester.tap(find.byType(UsageCardView));
        await tester.pumpAndSettle();
        await _shot(tester, key, '$dir/mobile-sheet-$suffix.png');
        usageRemaining.value = true;
        await tester.pumpAndSettle();
        await _shot(tester, key, '$dir/mobile-sheet-left-$suffix.png');
        usageRemaining.value = false;
      }
    }
    // 12 小时制：超过一天的重置时间写成 6:04 PM、下午6:04
    tester.platformDispatcher.alwaysUse24HourFormatTestValue = false;
    tester.binding.handleAccessibilityFeaturesChanged();
    for (final lang in ['zh', 'en']) {
      final key = GlobalKey();
      await tester.pumpWidget(
        RepaintBoundary(
          key: key,
          child: MaterialApp(
            debugShowCheckedModeBanner: false,
            theme: posseTheme(Brightness.light),
            locale: Locale(lang),
            localizationsDelegates: AppLocalizations.localizationsDelegates,
            supportedLocales: AppLocalizations.supportedLocales,
            home: _Home(usages: _sample(now)),
          ),
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byType(UsageCardView));
      await tester.pumpAndSettle();
      final suffix = lang == 'zh' ? '' : '-$lang';
      await _shot(tester, key, '$dir/mobile-sheet-12h-light$suffix.png');
    }
  }, skip: dir.isEmpty);
}
