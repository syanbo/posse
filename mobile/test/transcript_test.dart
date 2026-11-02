import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/chat/models.dart';
import 'package:posse_mobile/chat/transcript.dart';
import 'package:flutter/widgets.dart' show Locale;
import 'package:posse_mobile/l10n/l10n.dart';

final zh = lookupAppLocalizations(const Locale('zh'));
final en = lookupAppLocalizations(const Locale('en'));

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

  test('a queued slash command written twice shows once', () {
    final entries = foldTranscript([
      message('p', 'user', const [
        ChatBlock(type: 'text', text: '/compact'),
      ], timestamp: 1000),
      message('w', 'user', const [
        ChatBlock(type: 'text', text: '/compact'),
      ], timestamp: 1005),
      message('a', 'assistant', const [ChatBlock(type: 'text', text: '好')]),
      // 隔了一会儿再说同一句是两次
      message('again', 'user', const [
        ChatBlock(type: 'text', text: '/compact'),
      ], timestamp: 60000),
    ]);

    expect(entries.map((entry) => entry.message?.id), ['p', 'a', 'again']);
  });

  test('time label matches the history detail clock', () {
    final at = DateTime(2026, 10, 1, 15, 4);
    expect(
      chatTimeLabel(zh, at.millisecondsSinceEpoch, h24: true),
      '10月1日 15:04',
    );
    expect(
      chatTimeLabel(en, at.millisecondsSinceEpoch, h24: true),
      'Oct 1 15:04',
    );
    expect(
      chatTimeLabel(
        zh,
        DateTime(2026, 1, 2, 8, 5).millisecondsSinceEpoch,
        h24: true,
      ),
      '1月2日 08:05',
    );
    expect(chatTimeLabel(zh, null, h24: true), isNull);
    expect(chatTimeLabel(zh, 0, h24: true), isNull);
    expect(
      chatTimeLabel(zh, at.millisecondsSinceEpoch, h24: false),
      '10月1日 下午3:04',
    );
    expect(
      chatTimeLabel(en, at.millisecondsSinceEpoch, h24: false),
      'Oct 1 3:04 PM',
    );
    expect(chatWho(zh, 'user', 'grok'), '你');
    expect(chatWho(zh, 'assistant', 'grok'), 'Grok');
  });
}
