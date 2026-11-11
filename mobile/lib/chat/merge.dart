import 'models.dart';

/// 手机自己判断出的会话流报错，存代号，显示时由会话页按界面语言写（[AppliedFrame.error]）；
/// 电脑发来的报错已按手机的语言翻好，原样存。
const chatReadFailed = 'chat.readFailed';

class ChatMerger {
  List<ChatMessage> list = [];
  final indexById = <String, int>{};
}

class AppliedFrame {
  const AppliedFrame.messages({
    required this.messages,
    this.hasMore,
    this.beforeOffset,
    this.cursorInvalidated = false,
    this.windowReplaced = false,
    this.pending = false,
  }) : kind = 'messages',
       error = null;

  const AppliedFrame.error(this.error)
    : kind = 'error',
      messages = const [],
      pending = false,
      windowReplaced = false,
      cursorInvalidated = false,
      hasMore = null,
      beforeOffset = null;

  const AppliedFrame.ignored()
    : kind = 'ignored',
      messages = const [],
      error = null,
      pending = false,
      windowReplaced = false,
      cursorInvalidated = false,
      hasMore = null,
      beforeOffset = null;

  final String kind;
  final List<ChatMessage> messages;
  final String? error;
  final bool? hasMore;
  final int? beforeOffset;
  final bool cursorInvalidated;
  final bool windowReplaced;
  final bool pending;
}

void replaceList(ChatMerger merger, List<ChatMessage> messages) {
  merger.list = List<ChatMessage>.of(messages);
  merger.indexById
    ..clear()
    ..addEntries([
      for (var i = 0; i < merger.list.length; i++)
        MapEntry(merger.list[i].id, i),
    ]);
}

List<ChatMessage> boundWindow(List<ChatMessage> messages, int limit) {
  if (limit <= 0 || messages.length <= limit) return messages;
  var start = messages.length - limit;
  while (start > 0 &&
      shareTranscriptRow(messages[start - 1], messages[start])) {
    start--;
  }
  return messages.sublist(start);
}

void _applyIncoming(
  List<ChatMessage> list,
  Map<String, int> indexById,
  List<ChatMessage> incoming,
) {
  for (final message in incoming) {
    final at = indexById[message.id];
    if (at == null) {
      indexById[message.id] = list.length;
      list.add(message);
      continue;
    }
    if (sourceRank(message.source) >= sourceRank(list[at].source)) {
      list[at] = message;
    }
  }
}

List<ChatMessage> applyAppend(
  ChatMerger merger,
  List<ChatMessage> incoming, {
  int? limit,
}) {
  if (incoming.isEmpty) return merger.list;
  final next = List<ChatMessage>.of(merger.list);
  _applyIncoming(next, merger.indexById, incoming);
  final bounded = limit == null ? next : boundWindow(next, limit);
  if (!identical(bounded, next)) {
    replaceList(merger, bounded);
    return merger.list;
  }
  merger.list = next;
  return next;
}

int? _replayStart(
  ChatMerger merger,
  List<ChatMessage> messages,
  bool? hasMore,
) {
  if (messages.isEmpty) return null;
  final firstIndex = merger.indexById[messages.first.id];
  if (firstIndex == null) return null;
  if (hasMore == false && firstIndex > 0) return null;
  var expected = firstIndex;
  var sawNew = false;
  for (final message in messages) {
    final existing = merger.indexById[message.id];
    if (existing == null) {
      sawNew = true;
    } else if (sawNew || existing != expected) {
      return null;
    } else {
      expected++;
    }
  }
  return expected == merger.list.length ? firstIndex : null;
}

/// snapshot 能接上已保留的尾部就按 id 合并，接不上就整窗替换。
AppliedFrame applyStreamFrame({
  required ChatMerger merger,
  required Map<String, Object?> frame,
  required int limit,
  required bool replaceSnapshot,
}) {
  final type = frame['type'];
  if (type == 'error') {
    return AppliedFrame.error(
      frame['message'] as String? ??
          frame['error'] as String? ??
          chatReadFailed,
    );
  }
  if (type != 'snapshot' && type != 'replacement' && type != 'appended') {
    return const AppliedFrame.ignored();
  }
  if (frame['error'] is String) {
    return AppliedFrame.error(frame['error'] as String);
  }
  if (frame['messages'] is! List) return const AppliedFrame.ignored();
  final messages = parseMessages(frame['messages']);
  final pending = type == 'snapshot' && frame['pending'] == true;
  final hasMore = frame['hasMore'] is bool ? frame['hasMore'] as bool : null;
  final beforeOffset = frame['beforeOffset'] is num
      ? (frame['beforeOffset'] as num).toInt()
      : null;
  final replayStart =
      type == 'snapshot' && !replaceSnapshot && merger.list.isNotEmpty
      ? _replayStart(merger, messages, hasMore)
      : null;
  if (type == 'replacement' || (type == 'snapshot' && replayStart == null)) {
    replaceList(merger, messages);
    return AppliedFrame.messages(
      messages: merger.list,
      hasMore: hasMore,
      beforeOffset: beforeOffset,
      windowReplaced: true,
      pending: pending,
    );
  }
  final previousFirst = merger.list.isEmpty ? null : merger.list.first.id;
  final merged = applyAppend(merger, messages, limit: limit);
  final cursorInvalidated =
      previousFirst != null &&
      (merged.isEmpty || merged.first.id != previousFirst);
  final replayAtOldest = type == 'snapshot' && replayStart == 0;
  return AppliedFrame.messages(
    messages: merged,
    pending: pending,
    cursorInvalidated: cursorInvalidated,
    hasMore: type == 'snapshot' && cursorInvalidated
        ? true
        : replayAtOldest
        ? hasMore
        : null,
    beforeOffset: replayAtOldest && !cursorInvalidated ? beforeOffset : null,
  );
}

/// 往上翻页：更早的一页接在窗口前面。偏移倒回去了且没有共同 id，就整窗换成这一页。
List<ChatMessage> stitchOlder(
  List<ChatMessage> older,
  List<ChatMessage> window,
) {
  final byId = {for (final message in window) message.id: message};
  final seen = <String>{};
  final out = <ChatMessage>[];
  for (final message in older) {
    final current = byId[message.id];
    if (current == null ||
        sourceRank(message.source) >= sourceRank(current.source)) {
      out.add(message);
    } else {
      out.add(current);
    }
    seen.add(message.id);
  }
  for (final message in window) {
    if (!seen.contains(message.id)) out.add(message);
  }
  return out;
}

class ChatWindow {
  final merger = ChatMerger();
  int? beforeOffset;
  var hasMore = false;
  var pending = false;
  String? error;
  var _replaceSnapshot = true;

  List<ChatMessage> get messages => merger.list;

  void applyFrame(Map<String, Object?> frame, {int limit = 40}) {
    final type = frame['type'];
    final applied = applyStreamFrame(
      merger: merger,
      frame: frame,
      limit: limit,
      replaceSnapshot: _replaceSnapshot && type == 'snapshot',
    );
    if (type == 'snapshot') _replaceSnapshot = false;
    if (applied.kind == 'error') {
      error = applied.error;
      return;
    }
    if (applied.kind != 'messages') return;
    error = null;
    pending = applied.pending;
    if (applied.windowReplaced) {
      beforeOffset = applied.beforeOffset;
      hasMore = applied.hasMore ?? false;
    } else {
      if (applied.beforeOffset != null) beforeOffset = applied.beforeOffset;
      if (applied.cursorInvalidated) {
        hasMore = true;
        // 窗口顶上被裁掉了，往前翻要从现在的第一条往前读，不然裁掉的那段永远读不回来
        final first = merger.list.isEmpty
            ? null
            : merger.list.first.transcriptOffset;
        if (first != null) beforeOffset = first;
      } else if (applied.hasMore != null) {
        hasMore = applied.hasMore!;
      }
    }
  }

  void applyOlderPage(Map<String, Object?> page) {
    final older = parseMessages(page['messages']);
    if (older.isEmpty) return;
    if (olderPageDisjoint(older, merger.list)) {
      replaceList(merger, older);
    } else {
      replaceList(merger, stitchOlder(older, merger.list));
    }
    if (page['beforeOffset'] is num) {
      beforeOffset = (page['beforeOffset'] as num).toInt();
    }
    if (page['hasMore'] is bool) hasMore = page['hasMore'] as bool;
    pending = false;
  }
}

bool olderPageDisjoint(List<ChatMessage> older, List<ChatMessage> window) {
  if (older.isEmpty || window.isEmpty) return false;
  final ids = window.map((message) => message.id).toSet();
  final overlaps = older.any((message) => ids.contains(message.id));
  if (overlaps) return false;
  final pageLast = older.last.transcriptOffset;
  final windowFirst = window.first.transcriptOffset;
  return pageLast != null && windowFirst != null && pageLast >= windowFirst;
}
