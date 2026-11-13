import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/protocol/pairing.dart';
import 'package:posse_mobile/protocol/rpc.dart';
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

  test('Codex unknown preserves the reading and follows cached reset time', () {
    final now = DateTime(2026, 11, 12, 12);
    final status = UsageStatus.fromJson({
      'codexError': 'read failed',
      'codexReading': true,
      'usages': [
        {
          'agent': 'codex',
          'updatedAt': now
              .subtract(const Duration(minutes: 6))
              .millisecondsSinceEpoch,
          'windows': [
            {
              'percent': 96,
              'minutes': 300,
              'resetsAt': now
                  .subtract(const Duration(seconds: 1))
                  .millisecondsSinceEpoch,
            },
            {'percent': 81, 'minutes': 10080, 'unknown': true},
          ],
        },
      ],
    });
    expect(en.usagePendingUpdate, 'Pending update');
    expect(en.usageOldValue, 'Old reading');
    expect(zh.usageOldReading('6 分钟前'), '旧值 · 6 分钟前');
    expect(status.codexError, 'read failed');
    expect(status.codexReading, isTrue);
    expect(status.codexOff, isFalse);
    expect(UsageStatus.fromJson({'codexOff': true}).codexOff, isTrue);
    final u = status.usages.single;
    expect(u.windows.first.percent, 96);
    expect(u.windows.first.resetsAt, now.subtract(const Duration(seconds: 1)));
    expect(u.windowUnknown(u.windows.first, now), isTrue);
    expect(u.windowUnknown(u.windows.last, now), isTrue);
    expect(
      u.windowUnknown(
        u.windows.first,
        now.subtract(const Duration(minutes: 1)),
      ),
      isFalse,
    );
    expect(u.stale(now), isTrue);
    expect(u.stale(u.updatedAt.add(const Duration(minutes: 5))), isFalse);
    final claude = AgentUsage(
      agent: 'claude',
      windows: u.windows,
      updatedAt: u.updatedAt,
    );
    expect(claude.windowUnknown(claude.windows.first, now), isFalse);
    expect(claude.stale(now), isFalse);
  });

  testWidgets('unknown windows hide percentages and urgency in both modes', (
    tester,
  ) async {
    final now = DateTime.now();
    final u = AgentUsage(
      agent: 'codex',
      windows: [
        UsageWindow(
          percent: 96,
          minutes: 300,
          resetsAt: now.subtract(const Duration(minutes: 1)),
        ),
        UsageWindow(
          percent: 0,
          minutes: 10080,
          unknown: true,
          resetsAt: now.add(const Duration(hours: 2)),
        ),
      ],
      updatedAt: now.subtract(const Duration(minutes: 6)),
    );
    for (final remaining in [false, true]) {
      usageRemaining.value = remaining;
      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.light),
          home: Scaffold(body: UsageSheet(usages: [u])),
        ),
      );
      expect(find.text('待更新'), findsNWidgets(2));
      expect(find.textContaining('96%'), findsNothing);
      expect(find.text('已重置'), findsNothing);
      expect(find.textContaining('4%'), findsNothing);
      expect(find.textContaining('100%'), findsNothing);
      expect(find.textContaining('后重置'), findsNothing);
      for (final bar in tester.widgetList<UsageBar>(find.byType(UsageBar))) {
        expect(bar.percent, 0);
        expect(bar.urgent, isFalse);
      }
      expect(find.textContaining('旧值 · '), findsOneWidget);
      final label = tester.widget<Text>(find.text('待更新').first);
      expect(
        label.style?.color,
        isNot(posseTheme(Brightness.light).colorScheme.error),
      );
    }
    usageRemaining.value = false;
  });

  testWidgets('Codex failure without windows stays visible and localized', (
    tester,
  ) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.light),
        home: const Scaffold(
          body: UsageCardView(
            usages: [],
            codexError: 'unexpected upstream response',
          ),
        ),
      ),
    );
    expect(find.text('待更新'), findsOneWidget);
    await tester.tap(find.byType(UsageCardView));
    await tester.pumpAndSettle();
    expect(find.text('Codex'), findsOneWidget);
    expect(find.text('Codex 用量更新失败，请稍后重试。'), findsOneWidget);
    expect(find.textContaining('unexpected upstream response'), findsNothing);
  });

  testWidgets('opening details checks Codex and manual refresh still works', (
    tester,
  ) async {
    var automatic = 0;
    var manual = 0;
    final now = DateTime.now();
    final u = AgentUsage(
      agent: 'codex',
      windows: const [UsageWindow(percent: 17, minutes: 300)],
      updatedAt: now,
    );
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.light),
        home: Scaffold(
          body: UsageCardView(
            usages: [u],
            onCodexRefresh: () async {
              automatic++;
              return UsageStatus(usages: [u], codexError: 'read failed');
            },
            onRefresh: () async {
              manual++;
              return UsageStatus(usages: [u]);
            },
          ),
        ),
      ),
    );
    expect(automatic, 0);
    await tester.tap(find.byType(UsageCardView));
    await tester.pumpAndSettle();
    expect(automatic, 1);
    expect(manual, 0);
    expect(find.text('已用 17%'), findsOneWidget);
    expect(find.text('Codex 用量更新失败，请稍后重试。'), findsOneWidget);
    await tester.tap(find.byTooltip('刷新'));
    await tester.pumpAndSettle();
    expect(manual, 1);
    expect(find.text('Codex 用量更新失败，请稍后重试。'), findsNothing);
  });

  for (final hasCodex in [false, true]) {
    testWidgets('refresh keeps sheet and bars still with Codex=$hasCodex', (
      tester,
    ) async {
      final usages = [
        AgentUsage(
          agent: 'claude',
          windows: const [UsageWindow(percent: 42, minutes: 300)],
          updatedAt: DateTime.now(),
        ),
        if (hasCodex)
          AgentUsage(
            agent: 'codex',
            windows: const [UsageWindow(percent: 17, minutes: 300)],
            updatedAt: DateTime.now(),
          ),
      ];
      final automatic = Completer<UsageStatus>();
      final manual = Completer<UsageStatus>();
      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.light),
          home: Scaffold(
            body: UsageCardView(
              usages: usages,
              onCodexRefresh: () => automatic.future,
              onRefresh: () => manual.future,
            ),
          ),
        ),
      );
      await tester.tap(find.byType(UsageCardView));
      await tester.pump();
      await tester.pump(const Duration(seconds: 1));
      final sheet = tester.getRect(find.byType(BottomSheet));
      final bars = tester.getTopLeft(find.byType(UsageBar).last);
      expect(find.text('Codex'), hasCodex ? findsOneWidget : findsNothing);
      if (hasCodex) expect(find.text(zh.usageReading), findsOneWidget);
      automatic.complete(UsageStatus(usages: usages));
      await tester.pumpAndSettle();
      expect(tester.getRect(find.byType(BottomSheet)), sheet);
      expect(tester.getTopLeft(find.byType(UsageBar).last), bars);
      await tester.tap(find.byTooltip(zh.commonRefresh));
      await tester.pump();
      expect(tester.getRect(find.byType(BottomSheet)), sheet);
      expect(tester.getTopLeft(find.byType(UsageBar).last), bars);
      manual.complete(UsageStatus(usages: usages));
      await tester.pumpAndSettle();
      expect(tester.getRect(find.byType(BottomSheet)), sheet);
      expect(tester.getTopLeft(find.byType(UsageBar).last), bars);
      expect(tester.takeException(), isNull);
    });
  }

  testWidgets(
    'disabled Codex skips automatic queries and follows refresh state',
    (tester) async {
      var automatic = 0;
      var off = false;
      final usages = [
        AgentUsage(
          agent: 'codex',
          windows: const [UsageWindow(percent: 17, minutes: 300)],
          updatedAt: DateTime.now(),
        ),
      ];
      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.light),
          home: Scaffold(
            body: UsageCardView(
              usages: usages,
              codexOff: true,
              onCodexRefresh: () async {
                automatic++;
                return const UsageStatus();
              },
              onRefresh: () async => UsageStatus(usages: usages, codexOff: off),
            ),
          ),
        ),
      );
      await tester.tap(find.byType(UsageCardView));
      await tester.pumpAndSettle();
      expect(automatic, 0);
      expect(find.text(zh.usageQueryDisabled), findsOneWidget);
      await tester.tap(find.byTooltip('刷新'));
      await tester.pumpAndSettle();
      expect(find.text(zh.usageQueryDisabled), findsNothing);
      off = true;
      await tester.tap(find.byTooltip('刷新'));
      await tester.pumpAndSettle();
      expect(find.text(zh.usageQueryDisabled), findsOneWidget);
      expect(automatic, 0);
    },
  );

  testWidgets('disabled Codex does not create a card or sheet placeholder', (
    tester,
  ) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.light),
        home: Scaffold(
          body: UsageCardView(
            codexOff: true,
            usages: [
              AgentUsage(
                agent: 'claude',
                windows: const [UsageWindow(percent: 42, minutes: 300)],
                updatedAt: DateTime.now(),
              ),
            ],
          ),
        ),
      ),
    );
    expect(find.text('42%'), findsOneWidget);
    expect(find.text(zh.usagePendingUpdate), findsNothing);
    await tester.tap(find.byType(UsageCardView));
    await tester.pumpAndSettle();
    expect(find.text('Claude'), findsOneWidget);
    expect(find.text('Codex'), findsNothing);
    expect(find.text(zh.usageQueryDisabled), findsNothing);
    expect(find.text(zh.usagePendingUpdate), findsNothing);
  });

  testWidgets('Codex errors explain the required action without raw errors', (
    tester,
  ) async {
    final cases = {
      'codex: reset credits query timed out': zh.usageCodexDetailsFailed,
      'codex: login expired; run codex login again': zh.usageCodexLoginRequired,
      'codex: not logged in; run codex login': zh.usageCodexLoginRequired,
      'codex: invalid auth.json; run codex login again':
          zh.usageCodexLoginRequired,
      'codex: usage requires ChatGPT login; run codex login':
          zh.usageCodexLoginRequired,
      'codex: ChatGPT account ID is missing; run codex login again':
          zh.usageCodexLoginRequired,
      'codex: unsupported OAuth login; run codex login again':
          zh.usageCodexLoginRequired,
      'codex: usage requires file auth storage; keyring, auto and ephemeral storage are not supported':
          zh.usageCodexFileStorageRequired,
      'codex: could not read reset credits: context deadline exceeded':
          zh.usageCodexDetailsFailed,
      'codex: usage request returned HTTP 500': zh.usageCodexReadFailed,
      'socket closed': zh.errorConnectionLost,
    };
    for (final entry in cases.entries) {
      await tester.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.light),
          home: Scaffold(
            body: UsageSheet(
              key: ValueKey(entry.key),
              usages: const [],
              codexError: entry.key,
            ),
          ),
        ),
      );
      expect(find.text(entry.value), findsOneWidget);
      expect(find.text(entry.key), findsNothing);
    }
    expect(zh.usageCodexLoginRequired, contains('codex login'));
    expect(
      zh.usageCodexFileStorageRequired,
      contains('cli_auth_credentials_store'),
    );
    expect(zh.usageCodexDetailsFailed, contains('用量已更新'));
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
    Future<UsageStatus> refresh() async {
      if (fail) {
        throw RpcException('unknown_method', 'Unknown method: usage.refresh');
      }
      return UsageStatus(
        usages: [
          AgentUsage(
            agent: 'codex',
            plan: 'Plus',
            windows: const [UsageWindow(percent: 30, minutes: 300)],
            updatedAt: DateTime.now(),
          ),
        ],
      );
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
        'codexOff': true,
        'usages': [
          {
            'agent': 'claude',
            'windows': [
              {'percent': 42, 'minutes': 300},
            ],
          },
          {
            'agent': 'codex',
            'windows': [
              {
                'percent': 99,
                'minutes': 300,
                'resetsAt': DateTime.now()
                    .subtract(const Duration(minutes: 1))
                    .millisecondsSinceEpoch,
              },
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
    expect(find.text('99%'), findsNothing);
    expect(find.text('待更新'), findsOneWidget);
    await tester.tap(find.byType(UsageCardView));
    await tester.pumpAndSettle();
    expect(find.text(zh.usageQueryDisabled), findsOneWidget);
    expect(find.text(zh.usageNotConnected), findsNothing);
  });
}
