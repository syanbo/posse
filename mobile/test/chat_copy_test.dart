import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/chat/bubbles.dart';
import 'package:posse_mobile/chat/markdown_view.dart';
import 'package:posse_mobile/chat/models.dart';

void main() {
  testWidgets('会话和历史的消息卡片复制 Markdown 链接目标和代码内容', (tester) async {
    const address = 'https://example.com/a%2fb?first=1&second=2';
    const code = 'if ready:\n    start()  \n\n';
    const blocks = [
      ChatBlock(type: 'text', text: '[**Release notes**]($address)'),
      ChatBlock(type: 'text', text: '```python\n$code```'),
    ];
    const message = ChatMessage(
      id: 'reply',
      role: 'assistant',
      blocks: blocks,
      timestamp: null,
      source: 'transcript',
    );
    final copied = <String>[];
    final messenger = tester.binding.defaultBinaryMessenger;
    messenger.setMockMethodCallHandler(SystemChannels.platform, (call) async {
      if (call.method == 'Clipboard.setData') {
        copied.add((call.arguments as Map)['text'] as String);
      }
      return null;
    });
    addTearDown(() {
      messenger.setMockMethodCallHandler(SystemChannels.platform, null);
    });

    for (final agent in ['claude', 'codex', 'cursor', 'grok', 'antigravity']) {
      await tester.pumpWidget(
        MaterialApp(
          key: ValueKey(agent),
          home: Scaffold(
            body: SelectionArea(
              child: MessageBubble(
                agent: agent,
                message: message,
                blocks: blocks,
              ),
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();
      await tester.longPress(find.text('Release notes', findRichText: true));
      await tester.pumpAndSettle();
      await tester.tap(find.byTooltip('复制代码'));
      await tester.pumpAndSettle();
      expect(copied.sublist(copied.length - 2), [address, code], reason: agent);
    }
  });

  testWidgets('列表和引用里的代码块也可复制原文', (tester) async {
    var copied = '';
    final messenger = tester.binding.defaultBinaryMessenger;
    messenger.setMockMethodCallHandler(SystemChannels.platform, (call) async {
      if (call.method == 'Clipboard.setData') {
        copied = (call.arguments as Map)['text'] as String;
      }
      return null;
    });
    addTearDown(() {
      messenger.setMockMethodCallHandler(SystemChannels.platform, null);
    });
    for (final data in [
      '> ```shell\n>   printf hi  \n> ```',
      '- Run this:\n\n  ```shell\n    printf hi  \n  ```',
    ]) {
      await tester.pumpWidget(
        MaterialApp(
          home: Scaffold(
            body: SelectionArea(child: MarkdownView(data, copyActions: true)),
          ),
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byTooltip('复制代码'));
      await tester.pumpAndSettle();
      expect(copied, '  printf hi  \n');
      copied = '';
    }
  });
}
