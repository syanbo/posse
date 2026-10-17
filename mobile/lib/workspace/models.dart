const chatAgents = {'claude', 'codex', 'grok'};

class WorktreeRow {
  const WorktreeRow({
    required this.id,
    required this.repo,
    required this.name,
    required this.path,
    required this.branch,
    required this.status,
    required this.live,
  });

  final String id;
  final String repo;
  final String name;
  final String path;
  final String branch;
  final String status;
  final int live;

  String get subtitle {
    if (branch.isNotEmpty) return branch;
    return path;
  }
}

class SessionTab {
  const SessionTab({
    required this.worktreeId,
    required this.title,
    required this.terminalId,
    this.agent,
    this.state,
    this.sessionId,
    this.transcriptPath,
    this.toolName,
    this.prompt,
  });

  final String worktreeId;
  final String title;
  final String terminalId;
  final String? agent;
  final String? state;
  final String? sessionId;
  final String? transcriptPath;
  final String? toolName;
  final String? prompt;

  bool get eligible =>
      agent != null &&
      chatAgents.contains(agent) &&
      (sessionId?.isNotEmpty ?? false);

  SessionTab copyWith({
    String? state,
    String? toolName,
    String? prompt,
    bool clearPrompt = false,
  }) {
    return SessionTab(
      worktreeId: worktreeId,
      title: title,
      terminalId: terminalId,
      agent: agent,
      state: state ?? this.state,
      sessionId: sessionId,
      transcriptPath: transcriptPath,
      toolName: clearPrompt ? null : toolName ?? this.toolName,
      prompt: clearPrompt ? null : prompt ?? this.prompt,
    );
  }
}

List<WorktreeRow> parseWorktrees(Object? raw) {
  if (raw is! List) return const [];
  final out = <WorktreeRow>[];
  for (final item in raw) {
    if (item is! Map) continue;
    final id = item['worktreeId'];
    if (id is! String || id.isEmpty) continue;
    out.add(
      WorktreeRow(
        id: id,
        repo: item['repo'] as String? ?? '',
        name: item['displayName'] as String? ?? id,
        path: item['path'] as String? ?? '',
        branch: item['branch'] as String? ?? '',
        status: item['status'] as String? ?? 'inactive',
        live: item['liveTerminalCount'] is num
            ? (item['liveTerminalCount'] as num).toInt()
            : 0,
      ),
    );
  }
  return out;
}

List<SessionTab> parseTabs(String worktreeId, Object? raw) {
  if (raw is! List) return const [];
  final out = <SessionTab>[];
  for (final item in raw) {
    if (item is! Map || item['type'] != 'terminal') continue;
    final terminal = item['terminal'];
    if (terminal is! String || terminal.isEmpty) continue;
    final status = item['agentStatus'];
    final agent =
        item['launchAgent'] as String? ??
        (status is Map ? status['agentType'] as String? : null);
    String? sessionId;
    String? transcriptPath;
    String? toolName;
    String? prompt;
    String? state;
    if (status is Map) {
      state = status['state'] as String?;
      toolName = status['toolName'] as String?;
      prompt = status['interactivePrompt'] as String?;
      final provider = status['providerSession'];
      if (provider is Map) {
        sessionId = provider['id'] as String?;
        transcriptPath = provider['transcriptPath'] as String?;
      }
    }
    out.add(
      SessionTab(
        worktreeId: worktreeId,
        title: item['title'] as String? ?? agent ?? 'shell',
        terminalId: terminal,
        agent: agent,
        state: state,
        sessionId: sessionId,
        transcriptPath: transcriptPath,
        toolName: toolName,
        prompt: prompt,
      ),
    );
  }
  return out;
}

/// 订阅帧里的标签。end、error，或帧里没有 tabs 时返回 null，调用方保留当前列表。
List<SessionTab>? tabsFromFrame(String worktreeId, Map<String, Object?> frame) {
  final type = frame['type'];
  if (type == 'end' || type == 'error') return null;
  if (frame['tabs'] is! List) return null;
  return parseTabs(worktreeId, frame['tabs']);
}

String statusLabel(String status) => switch (status) {
  'permission' => '待处理',
  'working' => '工作中',
  'done' => '完成',
  'active' => '空闲',
  _ => '没有会话',
};
