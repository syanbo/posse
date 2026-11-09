import 'dart:convert';
import 'dart:math';

import 'package:flutter/foundation.dart';

typedef DiscJson = Map<String, Object?>;
DiscJson discMap(Object? v) => v is Map ? v.cast<String, Object?>() : {};
List<DiscJson> discList(Object? v) => v is List ? v.map(discMap).toList() : [];
String discText(DiscJson v, String k) => v[k] is String ? v[k]! as String : '';
int discInt(DiscJson v, String k) => (v[k] as num?)?.toInt() ?? 0;
bool discMine(DiscJson v, String self) =>
    discText(v, 'owner').isEmpty || v['owner'] == self;
String discRequestId() =>
    base64UrlEncode(List.generate(24, (_) => Random.secure().nextInt(256)));

typedef DiscCall = Future<DiscJson> Function(String method, DiscJson params);

/// 讨论里的这个人（协作信息的 people 按指纹找）；本机讨论没有协作信息，给 null。
DiscJson? discPerson(DiscJson collab, String fp) => fp.isEmpty
    ? null
    : discList(collab['people']).where((p) => p['fp'] == fp).firstOrNull;

/// 消息是不是 Agent 写的（回复、提问）：它们留着 Agent 的名字和图标。
bool discAgentAuthor(DiscJson m) =>
    m['kind'] == 'reply' ||
    m['kind'] == 'question' ||
    discText(m, 'authorMember').isNotEmpty ||
    discText(m, 'authorAgent').isNotEmpty;

/// 人写的消息显示他现在的名字：消息里存的是发送时的。找不到这个人时用消息里的。
String discAuthorName(DiscJson m, DiscJson collab) {
  final saved = discText(m, 'authorName');
  if (discAgentAuthor(m)) return saved;
  final p = discPerson(collab, discText(m, 'authorId'));
  final latest = p == null ? '' : discText(p, 'name');
  return latest.isEmpty ? saved : latest;
}

/// 消息窗口与讨论版本分开：读旧历史时只更新状态，不把新增消息拼到旧页后面。
class DiscussionState extends ChangeNotifier {
  DiscussionState(this.call, this.room);
  final DiscCall call;
  final String room;
  DiscJson discussion = {};
  DiscJson collab = {};
  DiscJson live = {};
  List<DiscJson> messages = [];
  List<DiscJson> members = [];
  List<DiscJson> executions = [];
  List<DiscJson> keys = [];
  bool more = false;
  bool moreAfter = false;
  bool loading = false;
  Object? error;
  int rev = 0;
  int _generation = 0;
  bool _disposed = false;
  bool _polling = false;

  bool get latest => !moreAfter;
  int get first => messages.isEmpty ? 0 : discInt(messages.first, 'seq');
  int get last => messages.isEmpty ? 0 : discInt(messages.last, 'seq');

  static List<DiscJson> merge(List<DiscJson> old, List<DiscJson> updates) {
    final byId = {for (final row in old) discText(row, 'id'): row};
    for (final row in updates) {
      byId[discText(row, 'id')] = row;
    }
    return byId.values.toList();
  }

  Future<void> page({
    int before = 0,
    int after = 0,
    String message = '',
  }) async {
    final gen = ++_generation;
    loading = true;
    error = null;
    notifyListeners();
    try {
      final result = await call('show', {
        'discussion': room,
        'limit': 50,
        if (before > 0) 'before': before,
        if (after > 0) 'after': after,
        if (message.isNotEmpty) 'message': message,
      });
      if (_disposed || gen != _generation) return;
      final append = before > 0 || after > 0;
      final rows = discList(result['messages']);
      messages = append ? merge(messages, rows) : rows;
      messages.sort((a, b) => discInt(a, 'seq').compareTo(discInt(b, 'seq')));
      if (before > 0) {
        more = result['more'] == true;
      } else if (after > 0) {
        moreAfter = result['moreAfter'] == true;
      } else {
        more = result['more'] == true;
        moreAfter = result['moreAfter'] == true;
      }
      if (messages.length > 500) {
        if (before > 0) {
          messages = messages.take(500).toList();
          moreAfter = true;
        } else {
          messages = messages.skip(messages.length - 500).toList();
          more = true;
        }
      }
      _metadata(
        result,
        replace: !append && message.isEmpty,
        updateRev: !append && message.isEmpty,
      );
      loading = false;
    } catch (e) {
      if (_disposed || gen != _generation) return;
      error = e;
      loading = false;
    }
    if (!_disposed) notifyListeners();
  }

  void _metadata(
    DiscJson result, {
    bool replace = false,
    bool updateRev = true,
  }) {
    discussion = discMap(result['discussion']);
    if (updateRev) rev = discInt(discussion, 'rev');
    collab = discMap(result['collab']);
    live = discMap(result['live']);
    members = replace
        ? discList(result['members'])
        : merge(members, discList(result['members']));
    executions = replace
        ? discList(result['executions'])
        : merge(executions, discList(result['executions']));
    final visible = messages.map((m) => m['id']).toSet();
    executions.removeWhere(
      (x) =>
          !visible.contains(x['message']) &&
          ![
            'queued',
            'held',
            'blocked',
            'claimed',
            'starting',
            'running',
            'waiting',
          ].contains(x['state']),
    );
    if (replace || result.containsKey('key')) keys = discList(result['key']);
    for (final m in discList(result['messages'])) {
      keys.removeWhere((k) => k['id'] == m['id']);
      if (m['keyAt'] != null) keys.add(m);
    }
  }

  Future<void> poll() async {
    if (_disposed || _polling || loading || discussion.isEmpty) return;
    _polling = true;
    final gen = _generation;
    try {
      final result = await call('show', {
        'discussion': room,
        'since': rev,
        'limit': 50,
      });
      if (_disposed || gen != _generation) return;
      final wasLatest = latest;
      if (result['reset'] == true && !wasLatest && messages.isNotEmpty) {
        final refreshed = <DiscJson>[];
        var refreshedExecutions = discList(result['executions']);
        for (var start = first; start <= last; start += 100) {
          final end = (start + 100).clamp(0, last + 1);
          final oldPage = await call('show', {
            'discussion': room,
            'before': end,
            'limit': end - start,
          });
          if (_disposed || gen != _generation) return;
          refreshed.addAll(discList(oldPage['messages']));
          refreshedExecutions = merge(
            refreshedExecutions,
            discList(oldPage['executions']),
          );
        }
        result['messages'] = refreshed;
        result['executions'] = refreshedExecutions;
      }
      if (result['reset'] == true && wasLatest) {
        messages = discList(result['messages']);
        more = result['more'] == true;
        moreAfter = result['moreAfter'] == true;
      } else {
        final updates = discList(result['messages']);
        messages = merge(
          messages,
          updates
              .where(
                (m) =>
                    messages.any((old) => old['id'] == m['id']) ||
                    wasLatest && discInt(m, 'seq') > last,
              )
              .toList(),
        );
        messages.sort((a, b) => discInt(a, 'seq').compareTo(discInt(b, 'seq')));
        if (messages.length > 500) {
          messages = messages.skip(messages.length - 500).toList();
          more = true;
        }
      }
      _metadata(result, replace: result['reset'] == true);
      if (!wasLatest) moreAfter = last < discInt(discussion, 'msgSeq');
    } catch (e) {
      if (!_disposed && gen == _generation) error = e;
    } finally {
      _polling = false;
    }
    if (!_disposed && gen == _generation) notifyListeners();
  }

  void dismissError() {
    error = null;
    notifyListeners();
  }

  @override
  void dispose() {
    _disposed = true;
    _generation++;
    super.dispose();
  }
}
