import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/chat/anchor.dart';
import 'package:hive_mobile/chat/models.dart';
import 'package:hive_mobile/chat/transcript.dart';

ChatMessage message(String id, String role, List<ChatBlock> blocks) {
  return ChatMessage(
    id: id,
    role: role,
    blocks: blocks,
    timestamp: null,
    source: 'transcript',
  );
}

ChatBlock call(String id) =>
    ChatBlock(type: 'tool-call', name: 'Read', callId: id);

/// 键和内容量都按倒序给（下标 0 是底部），和对话页一致。
ChatTail tail(List<(String, int)> rows) =>
    ChatTail([for (final r in rows) r.$1], [for (final r in rows) r.$2]);

void main() {
  test('工具组多了调用身份不变，同一条消息切开的几段各有身份', () {
    final before = transcriptKeys(
      foldTranscript([
        message('u', 'user', const [ChatBlock(type: 'text', text: '查')]),
        message('a', 'assistant', [call('c1')]),
      ]),
    );
    final after = transcriptKeys(
      foldTranscript([
        message('u', 'user', const [ChatBlock(type: 'text', text: '查')]),
        message('a', 'assistant', [call('c1')]),
        message('b', 'assistant', [call('c2')]),
      ]),
    );
    expect(before, ['m:u', 't:c1']);
    expect(after, before);

    final split = transcriptKeys(
      foldTranscript([
        message('a', 'assistant', [
          const ChatBlock(type: 'text', text: '先看'),
          call('c1'),
          const ChatBlock(type: 'text', text: '再改'),
        ]),
      ]),
    );
    expect(split, ['m:a', 't:c1', 'm:a#1']);

    final noIds = transcriptKeys(
      foldTranscript([
        message('u', 'user', const [ChatBlock(type: 'text', text: '查')]),
        message('t', 'tool', const [ChatBlock(type: 'tool-result')]),
      ]),
    );
    expect(noIds, ['m:u', 't:after:m:u']);
  });

  test('底部那行变高，参照它上面那行', () {
    final plan = anchorPlan(
      tail([('t:c1', 10), ('m:a', 5), ('m:u', 3)]),
      tail([('t:c1', 30), ('m:a', 5), ('m:u', 3)]),
    );
    expect(plan, const AnchorPlan(1, 1));
  });

  test('底部插进新消息，参照原来的最后一行', () {
    final plan = anchorPlan(
      tail([('working', 4), ('m:a', 5), ('m:u', 3)]),
      tail([('working', 4), ('m:b', 9), ('m:a', 5), ('m:u', 3)]),
    );
    expect(plan, const AnchorPlan(1, 2));
  });

  test('回显换成真消息、下标不变也要参照上面那行', () {
    final plan = anchorPlan(
      tail([('echo:1', 4), ('m:a', 5)]),
      tail([('m:u2', 4), ('m:a', 5)]),
    );
    expect(plan, const AnchorPlan(1, 1));
  });

  test('转圈行收起，参照往下挪一格', () {
    final plan = anchorPlan(
      tail([('working', 4), ('m:a', 5), ('m:u', 3)]),
      tail([('m:a', 5), ('m:u', 3)]),
    );
    expect(plan, const AnchorPlan(1, 0));
  });

  test('只在顶部加更早的消息、或者什么都没变，不修正', () {
    expect(
      anchorPlan(
        tail([('m:b', 5), ('m:a', 3)]),
        tail([('m:b', 5), ('m:a', 3), ('m:z', 8)]),
      ),
      isNull,
    );
    expect(anchorPlan(tail([('m:a', 3)]), tail([('m:a', 3)])), isNull);
    expect(anchorPlan(tail([]), tail([('m:a', 3)])), isNull);
  });

  test('翻远了，参照行从已经布局的第一行往上找', () {
    final plan = anchorPlan(
      tail([('t:c1', 10), ('m:a', 5), ('m:b', 7), ('m:c', 3)]),
      tail([('t:c1', 30), ('m:a', 5), ('m:b', 7), ('m:c', 3)]),
      from: 2,
    );
    expect(plan, const AnchorPlan(2, 2));
    final inserted = anchorPlan(
      tail([('t:c1', 10), ('m:a', 5), ('m:b', 7)]),
      tail([('m:n', 4), ('t:c1', 10), ('m:a', 5), ('m:b', 7)]),
      from: 2,
    );
    expect(inserted, const AnchorPlan(2, 3));
  });

  test('上面没有不变的行可参照时不修正', () {
    expect(anchorPlan(tail([('m:a', 3)]), tail([('m:a', 9)])), isNull);
  });
}
