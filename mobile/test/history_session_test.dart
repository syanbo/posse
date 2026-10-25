import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/history/history.dart';
import 'package:posse_mobile/history/models.dart';

import 'chat_scroll_test.dart' show FakeRpc, FakeModel, text, para;

void main() {
  testWidgets('往上翻到头自动读更早的一页，不用点按钮', (tester) async {
    tester.view.physicalSize = const Size(1080, 2340);
    tester.view.devicePixelRatio = 3;
    addTearDown(tester.view.reset);
    final fake = FakeRpc();
    final pages = <Object?>[];
    fake.readSession = (params) {
      pages.add(params['beforeOffset']);
      if (params['beforeOffset'] == null) {
        return {
          'messages': [
            for (var i = 20; i < 40; i++)
              text('m$i', i.isEven ? 'user' : 'assistant', para(i), i * 10),
          ],
          'hasMore': true,
          'beforeOffset': 200,
        };
      }
      return {
        'messages': [text('first', 'user', '最开始的提示。', 0)],
        'hasMore': false,
        'beforeOffset': 0,
      };
    };
    await tester.pumpWidget(
      MaterialApp(
        home: HistorySessionScreen(
          model: FakeModel(fake),
          session: HistorySession(
            agent: 'claude',
            id: 's1',
            title: '历史',
            cwd: '/w',
            branch: '',
            updatedAt: DateTime(2026, 10, 20),
            messages: 21,
            path: '/w/s1.jsonl',
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(pages, [null]);
    for (var i = 0; i < 12 && pages.length < 2; i++) {
      await tester.fling(find.byType(ListView), const Offset(0, 1200), 3000);
      await tester.pumpAndSettle();
    }
    expect(pages, [null, 200]);
    expect(find.text('更早的消息'), findsNothing);
    expect(find.textContaining('最开始的提示', findRichText: true), findsWidgets);
  });
}
