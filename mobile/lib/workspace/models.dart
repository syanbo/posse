import '../ui/widgets.dart';

const chatAgents = {'claude', 'codex', 'grok'};

class WorktreeRow {
  const WorktreeRow({
    required this.id,
    required this.repo,
    required this.name,
    required this.path,
    required this.branch,
    required this.live,
    this.terminals = 0,
    this.isMain = false,
    this.isWorktree = false,
    this.lastActivityAt = 0,
    this.iconText = '',
    this.iconImage = '',
  });

  final String id;
  final String repo;
  final String name;
  final String path;
  final String branch;
  final int live;

  /// 列得出来的终端数：在跑的加上一个常驻进程留下还没恢复的。旧常驻进程不给，和 [live] 一样。
  final int terminals;
  final bool isMain;

  /// git worktree：posse 建的，或从别处加进来的。和桌面一样用分支图标。
  final bool isWorktree;
  final int lastActivityAt;

  /// 桌面侧栏给项目设的图标：文字或 emoji，或者一张图片的文件名（经 posse.projectIcon 取）。都空是默认的文件夹。
  final String iconText;
  final String iconImage;

  /// 每个字段都一样。定时轮询拉回来的和上次一样时不通知界面（[sameWorktrees]），加字段要加在这里。
  bool sameAs(WorktreeRow o) =>
      id == o.id &&
      repo == o.repo &&
      name == o.name &&
      path == o.path &&
      branch == o.branch &&
      live == o.live &&
      terminals == o.terminals &&
      isMain == o.isMain &&
      isWorktree == o.isWorktree &&
      lastActivityAt == o.lastActivityAt &&
      iconText == o.iconText &&
      iconImage == o.iconImage;
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
    this.posse,
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

  /// 和桌面侧栏一样的状态（starting、tasks、unread、lost 等），旧常驻进程不给。只用来显示，见 [shownStateOf]。
  final String? posse;

  /// 首页和会话顶栏画的状态。
  AgentState get shown => shownStateOf(state, posse);

  bool get eligible =>
      agent != null &&
      chatAgents.contains(agent) &&
      (sessionId?.isNotEmpty ?? false);

  /// 每个字段都一样，见 [WorktreeRow.sameAs]。加字段要加在这里。
  bool sameAs(SessionTab o) =>
      worktreeId == o.worktreeId &&
      title == o.title &&
      terminalId == o.terminalId &&
      agent == o.agent &&
      state == o.state &&
      sessionId == o.sessionId &&
      transcriptPath == o.transcriptPath &&
      toolName == o.toolName &&
      prompt == o.prompt &&
      posse == o.posse;
}

/// 两份工作区列表逐项、按顺序完全一样。
bool sameWorktrees(List<WorktreeRow> a, List<WorktreeRow> b) {
  if (a.length != b.length) return false;
  for (var i = 0; i < a.length; i++) {
    if (!a[i].sameAs(b[i])) return false;
  }
  return true;
}

/// 两份各工作区的标签完全一样：同样的工作区、各自的标签逐项按顺序一样。
bool sameTabs(
  Map<String, List<SessionTab>> a,
  Map<String, List<SessionTab>> b,
) {
  if (a.length != b.length) return false;
  for (final e in a.entries) {
    final other = b[e.key];
    if (other == null || other.length != e.value.length) return false;
    for (var i = 0; i < other.length; i++) {
      if (!e.value[i].sameAs(other[i])) return false;
    }
  }
  return true;
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
        live: item['liveTerminalCount'] is num
            ? (item['liveTerminalCount'] as num).toInt()
            : 0,
        terminals: switch (item['posseTerminalCount'] ??
            item['liveTerminalCount']) {
          final num n => n.toInt(),
          _ => 0,
        },
        isMain: item['isMainWorktree'] == true,
        // 旧常驻进程不报，按有没有分支猜
        isWorktree: item['isWorktree'] is bool
            ? item['isWorktree'] as bool
            : item['isMainWorktree'] != true &&
                  (item['branch'] as String? ?? '').isNotEmpty,
        lastActivityAt: item['lastActivityAt'] is num
            ? (item['lastActivityAt'] as num).toInt()
            : 0,
        iconText: switch (item['repoIcon']) {
          {'text': final String text} => text,
          _ => '',
        },
        iconImage: switch (item['repoIcon']) {
          {'image': final String image} => image,
          _ => '',
        },
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
        posse: item['posseStatus'] as String?,
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
