import '../l10n/l10n.dart';

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
  AppLocalizations l,
  List<HistorySession> sessions,
  DateTime now,
) {
  final today = DateTime(now.year, now.month, now.day);
  String label(DateTime? t) {
    if (t == null) return l.historyOlder;
    final day = DateTime(t.year, t.month, t.day);
    final days = today.difference(day).inDays;
    if (days <= 0) return l.historyToday;
    if (days == 1) return l.historyYesterday;
    if (days < 7) return l.historyLast7Days;
    if (days < 30) return l.historyLast30Days;
    return l.historyOlder;
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

/// 历史页的 agent 筛选，和桌面的分段一一对应；空串是全部。
const historyAgents = ['', 'claude', 'codex', 'cursor', 'grok'];

/// 照桌面历史页按文件夹分组：会话归到目录所在的项目（项目和它的工作区、worktree 取最长匹配），
/// 不在任何项目里的用目录名；组按其中最近的会话排先后，组内最近的在前。
/// [roots] 是（目录, 项目名）。
List<(String, List<HistorySession>)> groupHistoryByFolder(
  AppLocalizations l,
  List<HistorySession> sessions,
  List<(String, String)> roots,
) {
  String label(String cwd) {
    if (cwd.isEmpty) return l.historyUnknownFolder;
    var name = '';
    var best = -1;
    for (final (root, project) in roots) {
      if (root.isEmpty || root.length <= best) continue;
      final base = root.endsWith('/')
          ? root.substring(0, root.length - 1)
          : root;
      if (cwd == root || cwd.startsWith('$base/')) {
        name = project;
        best = root.length;
      }
    }
    if (name.isNotEmpty) return name;
    final parts = cwd.split('/').where((p) => p.isNotEmpty);
    return parts.isEmpty ? cwd : parts.last;
  }

  final sorted = [...sessions];
  // 没有时间的排最后；sort 不稳定，同一时刻的按原来的先后
  final order = {for (var i = 0; i < sorted.length; i++) sorted[i]: i};
  sorted.sort((a, b) {
    final x = a.updatedAt, y = b.updatedAt;
    final by = x == null || y == null
        ? (x == null ? 1 : 0) - (y == null ? 1 : 0)
        : y.compareTo(x);
    return by != 0 ? by : order[a]! - order[b]!;
  });
  final groups = <String, List<HistorySession>>{};
  for (final s in sorted) {
    groups.putIfAbsent(label(s.cwd), () => []).add(s);
  }
  return [for (final e in groups.entries) (e.key, e.value)];
}

/// 列表右边的时间：一小时内写几分钟前，今天写时刻，今年写月日，更早带年份。
String historyTime(AppLocalizations l, DateTime? t, DateTime now) {
  if (t == null) return '';
  if (now.difference(t) < const Duration(hours: 1)) return agoText(l, t, now);
  if (t.year == now.year && t.month == now.month && t.day == now.day) {
    return clockTime(l, t);
  }
  if (t.year == now.year) return monthDay(l, t);
  return yearMonthDay(l, t);
}
