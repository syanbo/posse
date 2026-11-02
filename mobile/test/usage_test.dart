import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/protocol/pairing.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/usage/usage.dart';
import 'package:shared_preferences/shared_preferences.dart';

final zh = lookupAppLocalizations(const Locale('zh'));
final en = lookupAppLocalizations(const Locale('en'));

void main() {
  test('parses usage.get', () {
    final us = parseUsages({
      'usages': [
        {
          'agent': 'codex',
          'plan': 'plus',
          'windows': [
            {'percent': 7, 'minutes': 300, 'resetsAt': 1792402032000},
            {'percent': 8.5, 'minutes': 10080},
          ],
          'updatedAt': 1792389117408,
        },
        {
          'agent': 'cursor',
          'windows': [
            {'percent': 8.1, 'minutes': 44640},
          ],
          'spend': {'used': 3255, 'limit': 40000},
          'updatedAt': 1792389117408,
        },
      ],
      'cursorOff': false,
    });
    expect(us.length, 2);
    expect(us[0].plan, 'Plus'); // 旧常驻进程给的小写，显示时首字母大写
    expect(us[0].windows[0].resetsAt?.millisecondsSinceEpoch, 1792402032000);
    expect(us[0].windows[1].resetsAt, isNull);
    expect(us[1].spend?.limit, 40000);
  });

  test('labels windows like the desktop', () {
    expect(windowLabel(zh, 300), '5 小时');
    expect(windowLabel(zh, 10080), '每周');
    expect(windowLabel(zh, 44640), '每月');
    expect(windowLabel(zh, 0), '本期');
    final now = DateTime(2026, 10, 20, 1, 0);
    String reset(AppLocalizations l, Duration d) =>
        resetText(l, now.add(d), now, h24: true);
    expect(resetText(zh, null, now, h24: true), '');
    expect(reset(zh, const Duration(minutes: 95)), '1 小时 35 分钟后重置');
    expect(reset(zh, const Duration(minutes: 120)), '2 小时后重置');
    expect(reset(zh, const Duration(minutes: 59, seconds: 30)), '60 分钟后重置');
    expect(reset(zh, const Duration(seconds: 10)), '1 分钟后重置');
    expect(reset(zh, Duration.zero), '已重置');
    expect(reset(zh, const Duration(minutes: -1)), '已重置');
    expect(
      resetText(zh, DateTime(2026, 10, 23, 18, 4), now, h24: true),
      '10月23日 18:04 重置',
    );
    expect(reset(en, const Duration(minutes: 95)), 'Resets in 1 hr 35 min');
    expect(reset(en, const Duration(minutes: 120)), 'Resets in 2 hr');
    expect(reset(en, const Duration(seconds: 10)), 'Resets in 1 min');
    expect(reset(en, const Duration(minutes: -1)), 'Already reset');
    expect(
      resetText(en, DateTime(2026, 10, 23, 18, 4), now, h24: true),
      'Resets Oct 23 18:04',
    );
    // 12 小时制
    final later = DateTime(2026, 10, 23, 18, 4);
    expect(resetText(en, later, now, h24: false), 'Resets Oct 23 6:04 PM');
    expect(resetText(zh, later, now, h24: false), '10月23日 下午6:04 重置');
  });

  testWidgets('sheet shows every window and the spend', (tester) async {
    final now = DateTime.now();
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.light),
        home: Scaffold(
          body: UsageSheet(
            usages: [
              AgentUsage(
                agent: 'claude',
                windows: [
                  UsageWindow(
                    percent: 86,
                    minutes: 300,
                    resetsAt: now.add(const Duration(hours: 2)),
                  ),
                  const UsageWindow(percent: 41, minutes: 10080),
                ],
                updatedAt: now,
              ),
              AgentUsage(
                agent: 'cursor',
                plan: 'Ultra',
                windows: const [
                  UsageWindow(percent: 3, minutes: 44640),
                  UsageWindow(percent: 1, minutes: 44640, name: 'Auto'),
                  UsageWindow(percent: 77, minutes: 44640, name: 'API'),
                ],
                updatedAt: now,
                spend: const UsageMoney(used: 8452, limit: 40000),
              ),
            ],
          ),
        ),
      ),
    );
    expect(find.text('已用 86%'), findsOneWidget);
    usageRemaining.value = true;
    await tester.pump();
    expect(find.text('剩余 14%'), findsOneWidget);
    usageRemaining.value = false;
    await tester.pump();
    expect(find.text('每周'), findsOneWidget);
    expect(find.text('Ultra'), findsOneWidget);
    // Cursor 照它的控制台只列两池，不写总的百分比
    expect(find.text('Cursor 模型'), findsOneWidget);
    expect(find.text('其他模型'), findsOneWidget);
    expect(find.text('已用 77%'), findsOneWidget);
    expect(find.text('已用 3%'), findsNothing);
    expect(find.text('套餐内用量 \$84.52 / \$400.00（21%）'), findsOneWidget);
  });

  testWidgets('a tall usage sheet still closes', (tester) async {
    tester.view.physicalSize = const Size(390 * 3, 844 * 3);
    tester.view.devicePixelRatio = 3;
    addTearDown(tester.view.reset);
    final now = DateTime.now();
    final usages = [
      for (final agent in ['claude', 'codex', 'grok', 'cursor'])
        AgentUsage(
          agent: agent,
          windows: [
            for (final m in [300, 10080, 44640, 0])
              UsageWindow(percent: 40, minutes: m),
          ],
          updatedAt: now,
        ),
    ];
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.light),
        home: Scaffold(body: UsageCardView(usages: usages)),
      ),
    );
    Future<void> open() async {
      await tester.tap(find.byType(UsageCardView));
      await tester.pumpAndSettle();
      expect(find.text('套餐用量'), findsOneWidget);
    }

    // 上面留着遮罩，点一下就关
    await open();
    await tester.tapAt(const Offset(195, 40));
    await tester.pumpAndSettle();
    expect(find.text('套餐用量'), findsNothing);
  });

  test('parses pools and facts', () {
    final u = parseUsages({
      'usages': [
        {
          'agent': 'claude',
          'windows': [
            {'percent': 15, 'minutes': 300},
            {'percent': 12, 'minutes': 10080, 'name': 'Fable', 'at': 1000},
          ],
          'shares': {
            'items': [
              {'name': 'Claude Code', 'percent': 92},
            ],
            'at': 1000,
          },
          'updatedAt': 3600000,
        },
        {
          'agent': 'codex',
          'windows': [
            {'percent': 7, 'minutes': 300},
          ],
          'resets': {'count': 3, 'expiresAt': 1792693790000},
          'updatedAt': 3600000,
        },
      ],
    });
    expect(windowName(zh, u[0].windows[1]), 'Fable 每周');
    expect(u[0].shownWindows.length, 2);
    expect(u[0].headWindow.name, '');
    final now = DateTime.fromMillisecondsSinceEpoch(3600000);
    expect(
      usageFacts(zh, u[0], now).single,
      startsWith('本周用量用在 Claude Code 92%（'),
    );
    expect(usageFacts(zh, u[1], now).single, startsWith('重置卡 3 张，最早一张 '));
  });

  testWidgets('sheet refresh swaps in the new readings', (tester) async {
    var fail = false;
    Future<List<AgentUsage>> refresh() async {
      if (fail) {
        throw Exception('method_not_found: Unknown method: usage.refresh');
      }
      return [
        AgentUsage(
          agent: 'codex',
          plan: 'Plus',
          windows: const [UsageWindow(percent: 30, minutes: 300)],
          updatedAt: DateTime.now(),
        ),
      ];
    }

    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.light),
        home: Scaffold(
          body: UsageSheet(
            usages: [
              AgentUsage(
                agent: 'codex',
                windows: const [UsageWindow(percent: 4, minutes: 300)],
                updatedAt: DateTime.now(),
              ),
            ],
            onRefresh: refresh,
          ),
        ),
      ),
    );
    expect(find.text('已用 4%'), findsOneWidget);
    await tester.tap(find.byTooltip('刷新'));
    await tester.pumpAndSettle();
    expect(find.text('已用 30%'), findsOneWidget);
    expect(find.text('Plus'), findsOneWidget);
    fail = true;
    await tester.tap(find.byTooltip('刷新'));
    await tester.pumpAndSettle();
    expect(find.text('电脑上的 Posse 太旧，更新后才能刷新'), findsOneWidget);
  });

  testWidgets('the card shows the last readings before the computer answers', (
    tester,
  ) async {
    final code =
        'orca://pair?code=${base64Url.encode(utf8.encode(jsonEncode({'deviceToken': 't', 'endpoint': 'ws://a:1', 'publicKeyB64': 'k1', 'v': 2})))}';
    final offer = PairingOffer.parse(code);
    SharedPreferences.setMockInitialValues({
      'usage.cache.${computerId(offer)}': jsonEncode({
        'usages': [
          {
            'agent': 'claude',
            'windows': [
              {'percent': 42, 'minutes': 300},
            ],
          },
        ],
      }),
    });
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    model.offer = offer;
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: Scaffold(body: UsageCard(model: model)),
      ),
    );
    await tester.pumpAndSettle();
    // 还没连上（rpc 为空）就显示上次的读数，连上后原地换数，下面的列表不跳
    expect(model.rpc, isNull);
    expect(find.text('42%'), findsOneWidget);
  });
}
