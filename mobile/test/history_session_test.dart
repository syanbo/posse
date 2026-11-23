import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/history/history.dart';
import 'package:posse_mobile/history/models.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/workspace/models.dart';

import 'chat_scroll_test.dart' show FakeRpc, FakeModel, text, para;

void main() {
  testWidgets('没有目录的 Antigravity 历史可以选工作区后恢复', (tester) async {
    final rpc = _ResumeRpc();
    final model = FakeModel(rpc)
      ..worktrees = const [
        WorktreeRow(
          id: 'project::workspace',
          repo: 'Project',
          name: 'Target workspace',
          path: '/project',
          branch: '',
          live: 0,
        ),
      ];
    await tester.pumpWidget(
      MaterialApp(
        home: HistorySessionScreen(
          model: model,
          session: const HistorySession(
            agent: 'antigravity',
            id: 's1',
            title: 'Antigravity',
            cwd: '',
            branch: '',
            updatedAt: null,
            messages: 1,
            path: '/transcript.jsonl',
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(
      find.byWidgetPredicate((widget) => widget is FilledButton),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text('Project / Target workspace'));
    await tester.pumpAndSettle();
    expect(rpc.resume, [
      {'session': 's1', 'path': '/transcript.jsonl'},
      {'session': 's1', 'path': '/transcript.jsonl', 'workspace': 'workspace'},
    ]);
  });

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

class _ResumeRpc extends FakeRpc {
  final resume = <Map<String, Object?>>[];

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    if (method != 'history.resume') {
      return super.call(method, params, timeout: timeout);
    }
    resume.add(params);
    if (!params.containsKey('workspace')) {
      throw RpcException('workspace_required', 'A workspace is required');
    }
    return {'terminal': 't1', 'worktree': 'project::workspace'};
  }
}
