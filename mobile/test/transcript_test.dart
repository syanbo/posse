import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/chat/models.dart';
import 'package:hive_mobile/chat/transcript.dart';

ChatMessage message(
  String id,
  String role,
  List<ChatBlock> blocks, {
  int? timestamp,
}) {
  return ChatMessage(
    id: id,
    role: role,
    blocks: blocks,
    timestamp: timestamp,
    source: 'transcript',
  );
}

void main() {
  test('text stays a message and consecutive tools fold into one run', () {
    final entries = foldTranscript([
      message('u', 'user', const [ChatBlock(type: 'text', text: '你好')]),
      message('a', 'assistant', const [
        ChatBlock(type: 'text', text: '看一下'),
        ChatBlock(type: 'tool-call', name: '读文件'),
        ChatBlock(type: 'tool-call', name: '搜索'),
      ]),
      message('t', 'tool', const [
        ChatBlock(type: 'tool-result', output: 'src/a.go'),
      ]),
      message('b', 'assistant', const [ChatBlock(type: 'text', text: '改好了')]),
    ]);

    expect(entries, hasLength(4));
    expect(entries[0].tools, isFalse);
    expect(entries[0].blocks.single.text, '你好');
    expect(entries[1].blocks.single.text, '看一下');
    expect(entries[2].tools, isTrue);
    expect(toolRunCount(entries[2].blocks), 2);
    expect(entries[3].blocks.single.text, '改好了');
  });

  test('time label matches the history detail clock', () {
    final at = DateTime(2026, 10, 1, 15, 4);
    expect(chatTimeLabel(at.millisecondsSinceEpoch), '10月1日 15:04');
    expect(chatTimeLabel(null), isNull);
    expect(chatWho('user', 'grok'), '你');
    expect(chatWho('assistant', 'grok'), 'Grok');
  });
}
