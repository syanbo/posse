import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/ui/theme.dart';
import 'package:hive_mobile/usage/usage.dart';

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
    expect(us[0].plan, 'plus');
    expect(us[0].windows[0].resetsAt?.millisecondsSinceEpoch, 1792402032000);
    expect(us[0].windows[1].resetsAt, isNull);
    expect(us[1].spend?.limit, 40000);
  });

  test('labels windows like the desktop', () {
    expect(windowLabel(300), '5 小时');
    expect(windowLabel(10080), '每周');
    expect(windowLabel(44640), '每月');
    expect(windowLabel(0), '本期');
    final now = DateTime(2026, 10, 20, 1, 0);
    expect(
      resetText(now.add(const Duration(minutes: 95)), now),
      '1 小时 35 分钟后重置',
    );
    expect(resetText(now.subtract(const Duration(minutes: 1)), now), '已重置');
    expect(resetText(DateTime(2026, 10, 23, 18, 4), now), '10月23日 18:04 重置');
  });

  testWidgets('sheet shows every window and the spend', (tester) async {
    final now = DateTime.now();
    await tester.pumpWidget(
      MaterialApp(
        theme: hiveTheme(Brightness.light),
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
                windows: const [UsageWindow(percent: 8, minutes: 44640)],
                updatedAt: now,
                spend: const UsageMoney(used: 3255, limit: 40000),
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
    expect(find.text('每周'), findsOneWidget);
    expect(find.text('Ultra'), findsOneWidget);
    expect(find.text('套餐内已用 \$32.55，额度 \$400.00'), findsOneWidget);
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
    expect(windowName(u[0].windows[1]), 'Fable 每周');
    expect(u[0].mainWindows.length, 1);
    final now = DateTime.fromMillisecondsSinceEpoch(3600000);
    expect(usageFacts(u[0], now).single, startsWith('本周用量用在 Claude Code 92%（'));
    expect(usageFacts(u[1], now).single, startsWith('重置卡 3 张，最早一张 '));
  });
}
