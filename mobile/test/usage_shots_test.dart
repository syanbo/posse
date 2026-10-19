import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/ui/theme.dart';
import 'package:hive_mobile/ui/widgets.dart';
import 'package:hive_mobile/usage/usage.dart';

// 用量卡片和详情的离屏截图，给人看样子用：
// HIVE_SHOTS_DIR=/tmp/shots fvm flutter test test/usage_shots_test.dart
// 测试里没有真字体，字和图标都画成方块：正文借 macOS 的 Arial Unicode（中英文都有）顶替默认的 Roboto，
// 图标用 Flutter SDK 自带的 MaterialIcons。

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
    plan: 'Ultra',
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
    final c = HiveColors.of(context);
    Widget session(String agent, String title) => Padding(
      padding: const EdgeInsets.fromLTRB(16, 12, 16, 12),
      child: Row(
        children: [
          AgentTile(agent: agent, size: 22),
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
          'Hive',
          style: TextStyle(
            fontFamily: 'Roboto',
            fontSize: 26,
            fontWeight: FontWeight.w600,
            color: c.text,
          ),
        ),
        actions: [
          Icon(Icons.history_rounded, size: 24, color: c.text),
          const SizedBox(width: 16),
          Icon(Icons.add_circle_outline_rounded, size: 24, color: c.text),
          const SizedBox(width: 16),
          Icon(Icons.settings_outlined, size: 22, color: c.text),
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
                    Icon(Icons.folder_outlined, size: 22, color: c.accent),
                    const SizedBox(width: 12),
                    Text(
                      'hive',
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
  final dir = Platform.environment['HIVE_SHOTS_DIR'] ?? '';

  testWidgets('usage screenshots', (tester) async {
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
    final now = DateTime.now();
    for (final dark in [false, true]) {
      final key = GlobalKey();
      final theme = hiveTheme(dark ? Brightness.dark : Brightness.light);
      await tester.pumpWidget(
        RepaintBoundary(
          key: key,
          child: MaterialApp(
            debugShowCheckedModeBanner: false,
            theme: theme,
            home: _Home(usages: _sample(now)),
          ),
        ),
      );
      await tester.pumpAndSettle();
      final suffix = dark ? 'dark' : 'light';
      await _shot(tester, key, '$dir/mobile-home-$suffix.png');
      await tester.tap(find.byType(UsageCardView));
      await tester.pumpAndSettle();
      await _shot(tester, key, '$dir/mobile-sheet-$suffix.png');
      usageRemaining.value = true;
      await tester.pumpAndSettle();
      await _shot(tester, key, '$dir/mobile-sheet-left-$suffix.png');
      usageRemaining.value = false;
    }
  }, skip: dir.isEmpty);
}
