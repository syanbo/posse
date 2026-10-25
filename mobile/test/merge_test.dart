import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/chat/merge.dart';
import 'package:posse_mobile/chat/models.dart';
import 'package:posse_mobile/chat/prompt.dart';

ChatMessage message(
  String id, {
  String source = 'transcript',
  String text = '',
  int? offset,
  String role = 'assistant',
}) {
  return ChatMessage(
    id: id,
    role: role,
    blocks: [ChatBlock(type: 'text', text: text.isEmpty ? id : text)],
    timestamp: 1,
    source: source,
    transcriptOffset: offset,
  );
}

Map<String, Object?> frame(
  String type,
  List<ChatMessage> messages, {
  bool? hasMore,
  int? beforeOffset,
  bool? pending,
}) {
  return {
    'type': type,
    'messages': [
      for (final item in messages)
        {
          'id': item.id,
          'role': item.role,
          'source': item.source,
          'timestamp': item.timestamp,
          if (item.transcriptOffset != null)
            'transcriptOffset': item.transcriptOffset,
          'blocks': [
            for (final block in item.blocks)
              {'type': block.type, 'text': block.text},
          ],
        },
    ],
    'hasMore': ?hasMore,
    'beforeOffset': ?beforeOffset,
    'pending': ?pending,
  };
}

void main() {
  test('不限条数时追加不裁窗口，之后按条数再裁', () {
    final window = ChatWindow();
    window.applyFrame(
      frame('snapshot', [
        for (var i = 0; i < 3; i++) message('m$i', offset: 10 + i),
      ]),
      limit: 3,
    );
    window.applyFrame(frame('appended', [message('m3', offset: 13)]), limit: 0);
    expect(window.messages.map((item) => item.id), ['m0', 'm1', 'm2', 'm3']);
    window.applyFrame(frame('appended', [message('m4', offset: 14)]), limit: 3);
    expect(window.messages.map((item) => item.id), ['m2', 'm3', 'm4']);
    expect(window.hasMore, isTrue);
    // 往前翻从裁剩的第一条往前读
    expect(window.beforeOffset, 12);
  });

  test('same id replaces only when the source is not less authoritative', () {
    final window = ChatWindow();
    window.applyFrame(
      frame('snapshot', [message('a', text: 'old', source: 'transcript')]),
    );
    window.applyFrame(
      frame('appended', [
        message('a', text: 'scrape', source: 'scrape'),
        message('b'),
      ]),
    );
    expect(window.messages.map((item) => item.text), ['old', 'b']);
    window.applyFrame(
      frame('appended', [message('a', text: 'new', source: 'transcript')]),
    );
    expect(window.messages.first.text, 'new');
    expect(window.messages.map((item) => item.id), ['a', 'b']);
  });

  test('a snapshot that does not continue the window replaces it', () {
    final window = ChatWindow();
    window.applyFrame(
      frame(
        'snapshot',
        [message('old-0', offset: 1), message('old-1', offset: 2)],
        hasMore: true,
        beforeOffset: 1,
      ),
    );
    window.applyFrame(frame('appended', [message('live', offset: 3)]));
    window.applyFrame(
      frame(
        'snapshot',
        [
          message('old-1', offset: 2),
          message('live', offset: 3),
          message('more', offset: 4),
        ],
        hasMore: true,
        beforeOffset: 2,
      ),
    );
    expect(window.messages.map((item) => item.id), [
      'old-0',
      'old-1',
      'live',
      'more',
    ]);

    window.applyFrame(
      frame(
        'snapshot',
        [message('other', offset: 9)],
        hasMore: false,
        beforeOffset: 9,
      ),
    );
    expect(window.messages.map((item) => item.id), ['other']);
    expect(window.beforeOffset, 9);
    expect(window.hasMore, isFalse);
  });

  test('replacement replaces and an older page is prepended', () {
    final window = ChatWindow();
    window.applyFrame(
      frame(
        'snapshot',
        [message('b', offset: 20), message('c', offset: 30)],
        hasMore: true,
        beforeOffset: 20,
      ),
    );
    window.applyFrame(
      frame(
        'replacement',
        [message('c', offset: 30)],
        hasMore: false,
        beforeOffset: 30,
      ),
    );
    expect(window.messages.map((item) => item.id), ['c']);

    window.applyOlderPage({
      'messages': [
        {
          'id': 'a',
          'role': 'user',
          'source': 'transcript',
          'timestamp': 1,
          'transcriptOffset': 10,
          'blocks': [
            {'type': 'text', 'text': 'a'},
          ],
        },
      ],
      'beforeOffset': 10,
      'hasMore': false,
    });
    expect(window.messages.map((item) => item.id), ['a', 'c']);
    expect(window.beforeOffset, 10);
    expect(window.hasMore, isFalse);
  });

  test('an older page that overlaps the window by offset replaces it', () {
    final window = ChatWindow();
    window.applyFrame(frame('snapshot', [message('b', offset: 20)]));
    window.applyOlderPage({
      'messages': [
        {
          'id': 'z',
          'role': 'assistant',
          'source': 'transcript',
          'timestamp': 1,
          'transcriptOffset': 40,
          'blocks': [
            {'type': 'text', 'text': 'z'},
          ],
        },
      ],
      'beforeOffset': 40,
      'hasMore': false,
    });
    expect(window.messages.map((item) => item.id), ['z']);
  });

  test('the window bound keeps a shared transcript row', () {
    final merger = ChatMerger();
    replaceList(merger, [
      message('a', offset: 1),
      message('b', offset: 2, role: 'reasoning'),
      message('c', offset: 2),
    ]);
    final bounded = boundWindow(merger.list, 1);
    expect(bounded.map((item) => item.id), ['b', 'c']);
  });

  test('question steps and approval keys', () {
    expect(questionTiming(0).bodyAt, Duration.zero);
    expect(questionTiming(0).enterAt, const Duration(milliseconds: 500));
    expect(questionTiming(1).bodyAt, const Duration(milliseconds: 1000));
    expect(questionTiming(2).enterAt, const Duration(milliseconds: 2500));
    const question = AskQuestion(
      question: '选一个',
      multiSelect: true,
      options: [AskOption('甲'), AskOption('乙')],
    );
    expect(formatAnswer(question, ['甲', '乙']), '甲, 乙');
    expect(allowKey, '1');
    expect(denyKey, '\x1b');
    final approval = parseInteractivePrompt(
      '{"approval":{"tool":"Bash","summary":"go test"}}',
    );
    expect(approval?.approval?.tool, 'Bash');
    final ask = parseInteractivePrompt(
      '{"questions":[{"question":"q","header":"h","multiSelect":false,"options":[{"label":"甲","description":"d"}]}]}',
    );
    expect(ask?.questions.single.options.single.label, '甲');
  });

  test('answer steps wait half a second between body and enter', () async {
    final delays = <Duration>[];
    final writes = <String>[];
    await runAnswerSteps(
      lines: const ['甲', '乙'],
      writeBody: (line) async => writes.add(line),
      writeEnter: () async => writes.add('enter'),
      sleep: (delay) async => delays.add(delay),
    );
    expect(delays, [
      const Duration(milliseconds: 500),
      const Duration(milliseconds: 500),
      const Duration(milliseconds: 500),
    ]);
    expect(writes, ['甲', 'enter', '乙', 'enter']);
  });
}
