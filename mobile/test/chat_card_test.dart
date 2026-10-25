import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/chat/chat.dart';
import 'package:posse_mobile/chat/echo.dart';
import 'package:posse_mobile/chat/models.dart';
import 'package:posse_mobile/chat/prompt.dart';

ChatMessage line(String role, String text) {
  return ChatMessage(
    id: role,
    role: role,
    blocks: [ChatBlock(type: 'text', text: text)],
    timestamp: DateTime(2026, 10, 1, 15, 4).millisecondsSinceEpoch,
    source: 'transcript',
  );
}

void main() {
  testWidgets('user and agent lines follow the history detail header', (
    tester,
  ) async {
    final user = line('user', '你好');
    final agent = line('assistant', '看一下');
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: Column(
            children: [
              MessageBubble(message: user, blocks: user.blocks, agent: 'grok'),
              MessageBubble(
                message: agent,
                blocks: agent.blocks,
                agent: 'grok',
              ),
            ],
          ),
        ),
      ),
    );
    expect(find.text('你'), findsOneWidget);
    expect(find.text('Grok'), findsOneWidget);
    expect(find.text('10月1日 15:04'), findsNWidgets(2));
    expect(find.text('你好'), findsOneWidget);
    expect(find.text('看一下'), findsOneWidget);
  });

  testWidgets('approval card sends allow and deny without enter', (
    tester,
  ) async {
    final keys = <String>[];
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: ApprovalCard(
            approval: const Approval('Bash', 'go test'),
            busy: false,
            onDecision: keys.add,
          ),
        ),
      ),
    );
    expect(find.text('允许 Bash？'), findsOneWidget);
    expect(find.text('go test'), findsOneWidget);
    await tester.tap(find.text('允许'));
    await tester.tap(find.text('拒绝'));
    expect(keys, [allowKey, denyKey]);
  });

  testWidgets('question card submits one line per question', (tester) async {
    List<String>? lines;
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: QuestionCard(
            questions: const [
              AskQuestion(
                question: '第一题',
                header: '范围',
                options: [AskOption('甲'), AskOption('乙')],
              ),
              AskQuestion(
                question: '第二题',
                multiSelect: true,
                options: [AskOption('丙'), AskOption('丁')],
              ),
            ],
            onSubmit: (value) => lines = value,
          ),
        ),
      ),
    );
    expect(find.text('提交'), findsOneWidget);
    final button = tester.widget<FilledButton>(
      find.widgetWithText(FilledButton, '提交'),
    );
    expect(button.onPressed, isNull);
    await tester.tap(find.text('甲'));
    await tester.tap(find.text('丙'));
    await tester.tap(find.text('丁'));
    await tester.pump();
    await tester.tap(find.text('提交'));
    expect(lines, ['甲', '丙, 丁']);
  });

  testWidgets('刚发的话先画出来，进行中那一行照着状态行写', (tester) async {
    final echoes = LocalEchoes();
    final sending = echoes.add('改一下标题', images: 2);
    final sent = echoes.add('再跑一遍测试')..sent = true;
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: Column(
            children: [
              EchoBubble(echo: sending),
              EchoBubble(echo: sent),
              const WorkingRow(status: 'Puzzling… (12s · ↓ 1.2k tokens)'),
              const WorkingRow(tool: 'Bash'),
            ],
          ),
        ),
      ),
    );
    expect(find.text('发送中…'), findsOneWidget);
    expect(find.text('已发送'), findsOneWidget);
    expect(find.text('2 张图片'), findsOneWidget);
    expect(find.text('改一下标题'), findsOneWidget);
    expect(find.text('Puzzling… (12s · ↓ 1.2k tokens)'), findsOneWidget);
    expect(find.text('正在用 Bash…'), findsOneWidget);
  });
}
