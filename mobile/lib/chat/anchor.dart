import 'transcript.dart';

/// 倒序对话列表每一行的身份和内容量，下标 0 是视觉底部。
class ChatTail {
  const ChatTail(this.keys, this.sizes);

  final List<String> keys;
  final List<int> sizes;
}

/// 对话行的稳定身份，按正序给出。
///
/// 工具组随调用增加会变长，拿第一个调用 ID 认它；没有调用 ID 时认它前面那一行。
/// 同一条消息被工具调用切成几段时，后面的段带序号。
List<String> transcriptKeys(List<TranscriptEntry> entries) {
  final out = <String>[];
  final seen = <String, int>{};
  for (final entry in entries) {
    final message = entry.message;
    String key;
    if (!entry.tools && message != null && message.id.isNotEmpty) {
      key = 'm:${message.id}';
    } else {
      final first = entry.blocks
          .map((block) => block.callId)
          .whereType<String>()
          .firstWhere((id) => id.isNotEmpty, orElse: () => '');
      key = first.isNotEmpty
          ? 't:$first'
          : 't:after:${out.isEmpty ? '' : out.last}';
    }
    final n = seen.update(key, (v) => v + 1, ifAbsent: () => 0);
    out.add(n == 0 ? key : '$key#$n');
  }
  return out;
}

int entrySize(TranscriptEntry entry) {
  var size = 0;
  for (final block in entry.blocks) {
    size += block.text?.length ?? 0;
    size += block.output?.length ?? 0;
    size += block.name?.length ?? 0;
  }
  return size;
}

/// 变化前后都在、自身没变的参照行：before 是它变化前的下标，after 是变化后的。
class AnchorPlan {
  const AnchorPlan(this.before, this.after);

  final int before;
  final int after;

  @override
  bool operator ==(Object other) =>
      other is AnchorPlan && other.before == before && other.after == after;

  @override
  int get hashCode => Object.hash(before, after);

  @override
  String toString() => 'AnchorPlan($before, $after)';
}

/// 底部有行插入、删除、变高时，选一行在它上方且没变的参照行，布局后按它保持画面。
/// 只在顶部追加更早的消息、或者什么都没变时返回 null：倒序列表里这些不影响正在看的位置。
///
/// [from] 是当前已经布局的第一行。滚动观察器只量得到布局过的行，人往上翻远了，
/// 底部那几行早已不在，参照行要从这里往上找。
AnchorPlan? anchorPlan(ChatTail before, ChatTail after, {int from = 0}) {
  final common = before.keys.length < after.keys.length
      ? before.keys.length
      : after.keys.length;
  var d = 0;
  while (d < common &&
      before.keys[d] == after.keys[d] &&
      before.sizes[d] == after.sizes[d]) {
    d++;
  }
  if (d >= before.keys.length) return null;
  for (var i = d > from ? d : from; i < before.keys.length; i++) {
    final next = after.keys.indexOf(before.keys[i]);
    if (next == -1) continue;
    // 自己变高的那行不能当参照：它上面的内容照样会被推走
    if (before.sizes[i] != after.sizes[next]) continue;
    return AnchorPlan(i, next);
  }
  return null;
}
