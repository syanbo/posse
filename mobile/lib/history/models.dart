/// 电脑上的一个历史会话（常驻进程 history.list / history.search 的一项）。
class HistorySession {
  const HistorySession({
    required this.agent,
    required this.id,
    required this.title,
    required this.cwd,
    required this.branch,
    required this.updatedAt,
    required this.messages,
    required this.path,
    this.snippet,
    this.matches = 0,
  });

  final String agent;
  final String id;
  final String title;
  final String cwd;
  final String branch;
  final DateTime? updatedAt;
  final int messages;

  /// 会话文件，看内容时按它读（同一个 ID 可能对应几个不同的会话）。
  final String path;

  /// 搜索时命中的那条消息的摘要和命中的消息数。
  final String? snippet;
  final int matches;

  /// 会话目录的最后一段，列表里当项目名看。
  String get folder {
    final parts = cwd.split('/').where((p) => p.isNotEmpty).toList();
    return parts.isEmpty ? '' : parts.last;
  }

  static HistorySession? fromJson(Object? raw) {
    if (raw is! Map) return null;
    final id = raw['id'];
    final agent = raw['agent'];
    if (id is! String || id.isEmpty || agent is! String) return null;
    final updated = raw['updatedAt'];
    final snippet = raw['snippet'];
    return HistorySession(
      agent: agent,
      id: id,
      title: (raw['title'] as String? ?? '').trim(),
      cwd: raw['cwd'] as String? ?? '',
      branch: raw['branch'] as String? ?? '',
      updatedAt: updated is String
          ? DateTime.tryParse(updated)?.toLocal()
          : null,
      messages: (raw['messages'] as num?)?.toInt() ?? 0,
      path: raw['path'] as String? ?? '',
      snippet: snippet is String && snippet.trim().isNotEmpty
          ? snippet.trim()
          : null,
      matches: (raw['matches'] as num?)?.toInt() ?? 0,
    );
  }
}

/// history.list 回 sessions，history.search 回 hits；两种都认。
List<HistorySession> parseHistory(Map<String, Object?> result) {
  final raw = result['hits'] ?? result['sessions'];
  if (raw is! List) return const [];
  return [for (final item in raw) ?HistorySession.fromJson(item)];
}

/// 列表照桌面历史页按时间分组：今天、昨天、最近 7 天、最近 30 天、1 个月前。
List<(String, List<HistorySession>)> groupHistory(
  List<HistorySession> sessions,
  DateTime now,
) {
  final today = DateTime(now.year, now.month, now.day);
  String label(DateTime? t) {
    if (t == null) return '1 个月前';
    final day = DateTime(t.year, t.month, t.day);
    final days = today.difference(day).inDays;
    if (days <= 0) return '今天';
    if (days == 1) return '昨天';
    if (days < 7) return '最近 7 天';
    if (days < 30) return '最近 30 天';
    return '1 个月前';
  }

  final out = <(String, List<HistorySession>)>[];
  for (final s in sessions) {
    final l = label(s.updatedAt);
    if (out.isEmpty || out.last.$1 != l) {
      out.add((l, <HistorySession>[]));
    }
    out.last.$2.add(s);
  }
  return out;
}

/// 列表右边的时间：一小时内写几分钟前，今天写时刻，今年写月日，更早带年份。
String historyTime(DateTime? t, DateTime now) {
  if (t == null) return '';
  final d = now.difference(t);
  if (d.inMinutes < 1) return '刚刚';
  if (d.inHours < 1) return '${d.inMinutes} 分钟前';
  String two(int n) => n.toString().padLeft(2, '0');
  if (t.year == now.year && t.month == now.month && t.day == now.day) {
    return '${two(t.hour)}:${two(t.minute)}';
  }
  if (t.year == now.year) return '${t.month}月${t.day}日';
  return '${t.year}年${t.month}月${t.day}日';
}
