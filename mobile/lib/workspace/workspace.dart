import '../history/history.dart';
import '../ui/loading.dart';
import '../usage/usage.dart';

import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../app_state.dart';
import '../chat/chat.dart';
import '../desk/desk_page.dart';
import '../desk/plugin.dart';
import '../files/files.dart';
import '../git/git.dart';
import '../pair/scan.dart';
import '../protocol/pairing.dart';
import '../protocol/quic_socket.dart';
import '../protocol/rpc.dart';
import '../settings/settings.dart';
import '../terminal/terminal.dart';
import '../chat/latency_label.dart';
import '../ui/line_icons.dart';
import '../ui/theme.dart';
import '../update/update.dart';
import '../ui/widgets.dart';
import 'models.dart';
import 'new_session.dart';
import 'session_actions.dart';

class PairScreen extends StatefulWidget {
  const PairScreen({
    super.key,
    required this.model,
    this.adding = false,
    this.note,
    this.manual = false,
  });

  final PosseAppModel model;

  /// 已经连着一台电脑，再加一台：有返回按钮，连上新电脑后自动关掉。
  final bool adding;

  /// 从首页直接扫码没成功时带过来：为什么没成，要不要一进来就展开手动输入。
  final String? note;
  final bool manual;

  @override
  State<PairScreen> createState() => _PairScreenState();
}

class _PairScreenState extends State<PairScreen> {
  final _code = TextEditingController();
  late String? _note = widget.note;
  late var _manual = widget.manual;
  var _closing = false;

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_onModel);
  }

  void _onModel() {
    if (mounted && !_closing) setState(() {});
  }

  /// 扫码、粘贴、手动输入都走这里：连上了在底部提示连的是哪台电脑、走的哪个地址，
  /// 添加模式下回到首页（重新扫同一台电脑换地址也一样）；连不上的原因留在这一页上。
  Future<void> _connect(String code) async {
    setState(() => _note = null);
    final nav = Navigator.of(context);
    if (!await connectAndAnnounce(context, widget.model, code)) return;
    if (widget.adding && mounted && !_closing) {
      _closing = true;
      nav.pop();
    }
  }

  @override
  void dispose() {
    widget.model.removeListener(_onModel);
    _code.dispose();
    super.dispose();
  }

  Future<void> _scan() async {
    final pick = await scanPairingCode(context);
    if (!mounted) return;
    switch (pick.end) {
      case ScanEnd.code:
        await _connect(pick.code!);
      case ScanEnd.manual:
        setState(() => _manual = true);
      case ScanEnd.denied || ScanEnd.failed:
        setState(() => _note = scanNote(pick.end));
      case ScanEnd.cancelled || ScanEnd.unsupported:
        break;
    }
  }

  /// 剪贴板里是配对码就直接连，不是就展开输入框让用户自己贴。
  Future<void> _paste() async {
    final data = await Clipboard.getData(Clipboard.kTextPlain);
    final text = data?.text?.trim() ?? '';
    if (!mounted) return;
    if (isPairingCode(text)) {
      await _connect(text);
      return;
    }
    setState(() {
      _manual = true;
      _note = text.isEmpty ? '剪贴板是空的。' : '剪贴板里不是配对码。';
    });
  }

  @override
  Widget build(BuildContext context) {
    final model = widget.model;
    final c = PosseColors.of(context);
    final busy = model.connecting;
    final note = _note ?? model.error;
    return Scaffold(
      appBar: widget.adding ? AppBar() : null,
      body: SafeArea(
        child: ListView(
          padding: EdgeInsets.fromLTRB(24, widget.adding ? 8 : 56, 24, 24),
          children: [
            Align(
              alignment: Alignment.centerLeft,
              // 和桌面端、手机桌面上的 App 图标同一张图（design/icon-art.png）
              child: ClipRRect(
                borderRadius: BorderRadius.circular(14),
                child: Image.asset('assets/icon.png', width: 64, height: 64),
              ),
            ),
            const SizedBox(height: 24),
            Text(
              widget.adding ? '添加电脑' : '连接 Posse',
              style: TextStyle(
                fontSize: Fs.pageTitle,
                fontWeight: FontWeight.w600,
                color: c.text,
              ),
            ),
            const SizedBox(height: 8),
            Text(
              widget.adding
                  ? '可以配对多台电脑，同一时间连其中一台，在首页顶部切换。'
                  : '在手机上看 agent 的进展、回复和批准请求。',
              style: TextStyle(fontSize: Fs.body, height: 1.5, color: c.muted),
            ),
            const SizedBox(height: 28),
            Section(
              inset: 52,
              margin: const EdgeInsets.only(bottom: 20),
              children: [
                _step(context, 1, '在电脑上打开 Posse', '点窗口左下角的手机图标'),
                _step(context, 2, '扫描二维码', '或者在电脑上复制配对码，回来粘贴'),
              ],
            ),
            if (canScan) ...[
              FilledButton.icon(
                onPressed: busy ? null : _scan,
                icon: const Icon(Icons.qr_code_scanner_rounded, size: 20),
                label: Text(busy ? '正在连接…' : '扫码配对'),
              ),
              const SizedBox(height: 10),
            ],
            TextButton.icon(
              onPressed: busy ? null : _paste,
              icon: const Icon(Icons.content_paste_rounded, size: 18),
              label: const Text('粘贴配对码'),
            ),
            if (note != null) ...[const SizedBox(height: 12), NoticeBar(note)],
            const SizedBox(height: 8),
            Center(
              child: TextButton(
                style: TextButton.styleFrom(
                  backgroundColor: Colors.transparent,
                  foregroundColor: c.muted,
                ),
                onPressed: () => setState(() => _manual = !_manual),
                child: Text(_manual ? '收起' : '手动输入'),
              ),
            ),
            if (_manual) ...[
              TextField(
                controller: _code,
                minLines: 3,
                maxLines: 6,
                style: mono(13, color: c.text),
                decoration: const InputDecoration(
                  hintText: 'orca://pair?code=…',
                ),
              ),
              const SizedBox(height: 10),
              FilledButton(
                onPressed: busy ? null : () => _connect(_code.text),
                child: Text(busy ? '正在连接…' : '连接'),
              ),
            ],
          ],
        ),
      ),
    );
  }

  Widget _step(BuildContext context, int n, String title, String detail) {
    final c = PosseColors.of(context);
    return RowTile(
      chevron: false,
      leading: Container(
        width: 26,
        height: 26,
        alignment: Alignment.center,
        decoration: BoxDecoration(color: c.tint, shape: BoxShape.circle),
        child: Text(
          '$n',
          style: TextStyle(
            fontSize: 13,
            fontWeight: FontWeight.w600,
            color: c.accent,
          ),
        ),
      ),
      title: Text(title),
      subtitle: Text(detail),
    );
  }
}

/// 连上了在底部提示连的是哪台电脑、走的哪个地址。连不上的原因在 model.error。
Future<bool> connectAndAnnounce(
  BuildContext context,
  PosseAppModel model,
  String code,
) async {
  final messenger = ScaffoldMessenger.of(context);
  if (!await model.connectCode(code)) return false;
  final name = model.current == null ? '电脑' : model.labelOf(model.current);
  final where = hostOf(model.connectedUrl);
  messenger.showSnackBar(
    SnackBar(
      content: Text(where == null ? '已连上 $name' : '已连上 $name，地址 $where'),
    ),
  );
  return true;
}

String? scanNote(ScanEnd end) => switch (end) {
  ScanEnd.denied => '需要相机权限才能扫码：在系统设置里允许 Posse 使用相机，也可以把配对码贴进来。',
  ScanEnd.failed => '打不开相机，可以把配对码贴进来。',
  _ => null,
};

/// 地址里给人看的部分：主机加端口，路径不写。QUIC 标出来，经中转的不写中转的地址（令牌也不露）。
String? hostOf(String? url) {
  if (url == null) return null;
  final uri = Uri.tryParse(url);
  if (uri == null || uri.host.isEmpty) return null;
  if (isRelayQuicUrl(url)) return '经中转 · QUIC';
  if (isPunchedQuicUrl(url)) return '直连 · QUIC';
  final host = uri.hasPort ? '${uri.host}:${uri.port}' : uri.host;
  return isQuicUrl(url) ? '$host · QUIC' : host;
}

/// 工作区在列表里的名字：主工作区和项目同名时只写项目名。
String worktreeLabel(WorktreeRow row) {
  if (row.repo.isEmpty || row.name == row.repo) return row.name;
  return '${row.repo} / ${row.name}';
}

/// 打开一个会话：有原生存档的 agent 进对话页（右上角能切到终端），其余直接进终端。
void openSession(BuildContext context, PosseAppModel model, SessionTab tab) {
  final nav = Navigator.of(context);
  if (!tab.eligible) {
    nav.push(
      MaterialPageRoute<void>(
        builder: (_) => TerminalScreen(model: model, tab: tab),
      ),
    );
    return;
  }
  // 对话和终端二选一：顶栏的按钮在两者之间替换当前页，返回时回到打开会话之前的页面
  late Route<void> Function() chat;
  late Route<void> Function() terminal;
  chat = () => MaterialPageRoute<void>(
    builder: (_) => ChatScreen(
      model: model,
      tab: tab,
      onOpenTerminal: () => nav.pushReplacement(terminal()),
    ),
  );
  terminal = () => MaterialPageRoute<void>(
    builder: (_) => TerminalScreen(
      model: model,
      tab: tab,
      onOpenChat: () => nav.pushReplacement(chat()),
    ),
  );
  nav.push(model.settings.openMode == 'terminal' ? terminal() : chat());
}

/// 会话这一刻在做什么，一行话。
String sessionDetail(SessionTab tab) {
  return switch (tab.shown) {
    AgentState.waiting =>
      tab.toolName == null ? '等你处理' : '等你批准 ${tab.toolName}',
    AgentState.working => tab.toolName == null ? '工作中' : '正在用 ${tab.toolName}',
    AgentState.done => '本轮完成',
    AgentState.unread => '本轮完成，还没看',
    AgentState.tasks => '等待任务',
    AgentState.starting => '启动中',
    AgentState.lost => '未恢复，点开时拉起',
    _ => tab.agent == null ? '终端' : (tab.eligible ? '空闲' : '只能看终端'),
  };
}

/// 首页照桌面端侧栏：项目 → 工作区（只有主工作区时省掉这层）→ 运行中的 agent 会话，都直接展开。
/// 普通终端排在 agent 会话后面，也各占一行。
class WorkspaceScreen extends StatelessWidget {
  const WorkspaceScreen({super.key, required this.model});

  final PosseAppModel model;

  /// 直接开相机，扫到就连；取消就回首页。没权限、开不了相机、选了手动输入或连不上时
  /// 才进「添加电脑」页，带上原因。没有原生扫码的平台直接进那一页。
  Future<void> _addComputer(BuildContext context) async {
    final pick = await scanPairingCode(context);
    if (!context.mounted) return;
    String? note;
    switch (pick.end) {
      case ScanEnd.cancelled:
        return;
      case ScanEnd.code:
        if (await connectAndAnnounce(context, model, pick.code!)) return;
        if (!context.mounted) return;
        note = model.error ?? '连不上这台电脑。';
      case ScanEnd.denied || ScanEnd.failed:
        note = scanNote(pick.end);
      case ScanEnd.manual || ScanEnd.unsupported:
        break;
    }
    unawaited(
      Navigator.of(context).push(
        MaterialPageRoute<void>(
          builder: (_) => PairScreen(
            model: model,
            adding: true,
            note: note,
            manual: pick.end == ScanEnd.manual,
          ),
        ),
      ),
    );
  }

  void _showComputers(BuildContext context) {
    showModalBottomSheet<void>(
      context: context,
      showDragHandle: true,
      builder: (_) => _ComputerSheet(
        model: model,
        onAdd: () {
          Navigator.of(context).pop();
          _addComputer(context);
        },
        onDesk: () {
          Navigator.of(context).pop();
          Navigator.of(context)
              .push(DeskRoute(builder: (_) => DeskPage(model: model)));
        },
      ),
    );
  }

  void _openWorkspace(BuildContext context, WorktreeRow row) {
    Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => TabScreen(model: model, worktree: row),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final projects = <String, List<WorktreeRow>>{};
    for (final row in model.worktrees) {
      projects
          .putIfAbsent(row.repo.isEmpty ? row.name : row.repo, () => [])
          .add(row);
    }
    final names = model.orderProjects(projects.keys.toList());
    var waiting = 0;
    for (final list in model.tabs.values) {
      waiting += list.where((t) => _stateOf(t) == AgentState.waiting).length;
    }
    return Scaffold(
      appBar: AppBar(
        toolbarHeight: 60,
        // 胶囊左边和下面的用量条、项目卡片对齐
        titleSpacing: 10,
        title: Row(
          children: [
            // 胶囊用掉按钮以外的全部宽度、靠左，电脑名写得下就写全
            Expanded(
              child: Align(
                alignment: Alignment.centerLeft,
                child: Material(
                  color: c.fill,
                  borderRadius: BorderRadius.circular(16),
                  child: InkWell(
                    borderRadius: BorderRadius.circular(16),
                    onTap: () => _showComputers(context),
                    child: Padding(
                      padding: const EdgeInsets.fromLTRB(10, 6, 6, 6),
                      child: _ConnectionLine(model: model),
                    ),
                  ),
                ),
              ),
            ),
          ],
        ),
        actions: [
          // 刷新靠下拉；切换、忘掉、扫码添加电脑都在标题旁的电脑胶囊里，加电脑不常用，不占顶栏
          // 断线时历史和新建都点不了，收起来把位置让给电脑胶囊写全连接状态
          AnimatedSize(
            duration: const Duration(milliseconds: 260),
            curve: Curves.easeOutCubic,
            child: model.rpc == null
                ? const SizedBox.shrink()
                : Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      // 延迟排在按钮这一组最前面，左右各空 8，和图标按钮之间看着一样宽；
                      // 胶囊里的连接状态变宽变窄它不挪，断线重连时不显示
                      if (!model.connecting)
                        Padding(
                          padding: const EdgeInsets.symmetric(horizontal: 8),
                          child: LatencyLabel(
                            latency: model.latency,
                            unresponsive: model.unresponsive,
                            gap: 0,
                            size: 13,
                          ),
                        ),
                      IconButton(
                        tooltip: '历史',
                        visualDensity: VisualDensity.compact,
                        onPressed: () => Navigator.of(context).push(
                          MaterialPageRoute<void>(
                            builder: (_) => HistoryScreen(model: model),
                          ),
                        ),
                        // 圆形的图标墨迹比方形的小一圈，大两号才和设置看起来一样大
                        icon: Icon(
                          Icons.history_rounded,
                          size: 24,
                          color: c.text,
                        ),
                      ),
                      IconButton(
                        tooltip: '新建会话',
                        visualDensity: VisualDensity.compact,
                        onPressed: () => showNewSession(context, model),
                        icon: Icon(
                          Icons.add_circle_outline_rounded,
                          size: 24,
                          color: c.text,
                        ),
                      ),
                    ],
                  ),
          ),
          IconButton(
            tooltip: '设置',
            visualDensity: VisualDensity.compact,
            onPressed: () => Navigator.of(context).push(
              MaterialPageRoute<void>(
                builder: (_) => SettingsScreen(
                  model: model,
                  onComputers: () => _showComputers(context),
                ),
              ),
            ),
            icon: Icon(Icons.settings_outlined, size: 22, color: c.text),
          ),
          const SizedBox(width: 10),
        ],
      ),
      body: model.worktrees.isEmpty
          // 断线重连时还不知道电脑上有没有工作区，不说「还没有工作区」
          ? (model.connecting || (model.rpc == null && model.link != null)
                ? const Center(child: PosseLoader())
                : model.disconnected
                ? _DisconnectedList(
                    model: model,
                    onAdd: () => _addComputer(context),
                  )
                : EmptyState(
                    icon: model.error == null
                        ? Icons.folder_open_rounded
                        : Icons.cloud_off_rounded,
                    text: model.error ?? '电脑上的 Posse 还没有工作区',
                    error: model.error != null,
                    action: TextButton(
                      onPressed: model.retry,
                      child: const Text('重试'),
                    ),
                  ))
          // 长按项目卡片拖动排序，顺序按电脑分别存在手机上。
          // 报错浮在列表上面、断线写在电脑胶囊里，都不插进列表，免得列表跟着跳
          : Stack(
              children: [
                CustomScrollView(
                  physics: const BouncingScrollPhysics(
                    parent: AlwaysScrollableScrollPhysics(),
                  ),
                  slivers: [
                    SliverToBoxAdapter(
                      child: Padding(
                        padding: const EdgeInsets.only(top: 8),
                        child: Column(
                          children: [
                            UpdateBanner(model: model),
                            if (waiting > 0) _WaitingBanner(count: waiting),
                            UsageCard(model: model),
                          ],
                        ),
                      ),
                    ),
                    SliverReorderableList(
                      itemCount: names.length,
                      proxyDecorator: (child, _, animation) => AnimatedBuilder(
                        animation: animation,
                        builder: (context, child) => Transform.scale(
                          scale:
                              1 +
                              0.02 * Curves.easeOut.transform(animation.value),
                          child: Material(
                            color: Colors.transparent,
                            elevation: 8 * animation.value,
                            shadowColor: Colors.black.withValues(alpha: 0.3),
                            borderRadius: BorderRadius.circular(Rd.card),
                            child: child,
                          ),
                        ),
                        child: child,
                      ),
                      onReorderItem: (from, to) {
                        final order = List.of(names);
                        order.insert(to, order.removeAt(from));
                        model.setProjectOrder(order);
                      },
                      itemBuilder: (context, i) =>
                          ReorderableDelayedDragStartListener(
                            key: ValueKey(names[i]),
                            index: i,
                            child: Section(
                              inset: 48,
                              // 左右和卡片之间一样宽
                              margin: const EdgeInsets.fromLTRB(10, 0, 10, 10),
                              folded: model.isCollapsed(names[i]),
                              children: _project(
                                context,
                                names[i],
                                projects[names[i]]!,
                              ),
                            ),
                          ),
                    ),
                    const SliverToBoxAdapter(child: SizedBox(height: 32)),
                  ],
                ),
                Positioned(
                  top: 8,
                  left: 16,
                  right: 16,
                  child: _FloatingError(model: model),
                ),
              ],
            ),
    );
  }

  List<Widget> _project(
    BuildContext context,
    String name,
    List<WorktreeRow> rows,
  ) {
    final c = PosseColors.of(context);
    final collapsed = model.isCollapsed(name);
    // 只有主工作区：项目这一行就是工作区，会话直接挂在下面
    if (rows.length == 1) {
      final row = rows.single;
      final sessions = _sessions(context, row, indent: 48);
      return [
        _ProjectHeader(
          name: name,
          icon: _projectIcon(c, row),
          subtitle: model.settings.projectMetaHidden ? null : _where(c, row),
          count: row.live,
          expandable: sessions.isNotEmpty,
          collapsed: collapsed,
          state: _projectState(rows),
          onToggle: () => model.toggleProject(name),
          onOpen: () => _openWorkspace(context, row),
        ),
        ...sessions,
      ];
    }
    final main = rows.firstWhere((r) => r.isMain, orElse: () => rows.first);
    return [
      _ProjectHeader(
        name: name,
        icon: _projectIcon(c, main),
        subtitle: model.settings.projectMetaHidden
            ? null
            : Text(tildePath(main.path), style: mono(12, color: c.muted)),
        count: rows.fold(0, (n, r) => n + r.live),
        expandable: true,
        collapsed: collapsed,
        state: _projectState(rows),
        onToggle: () => model.toggleProject(name),
        onOpen: () => _openWorkspace(context, main),
      ),
      for (final row in rows) ...[
        _workspaceRow(context, row),
        ..._sessions(context, row, indent: 82),
      ],
    ];
  }

  /// 项目行首的图标，照桌面侧栏：设了文字或图片就用它，否则是文件夹。
  Widget _projectIcon(PosseColors c, WorktreeRow row) {
    if (row.iconText.isNotEmpty) {
      return SizedBox.square(
        dimension: 22,
        child: FittedBox(
          fit: BoxFit.scaleDown,
          child: Text(
            row.iconText,
            maxLines: 1,
            style: TextStyle(fontSize: 19, color: c.accent, height: 1),
          ),
        ),
      );
    }
    if (row.iconImage.isNotEmpty) {
      if (model.projectIcon(row.iconImage) case final bytes?) {
        return ClipRRect(
          borderRadius: BorderRadius.circular(4),
          child: Image.memory(
            bytes,
            width: 22,
            height: 22,
            fit: BoxFit.contain,
            gaplessPlayback: true,
          ),
        );
      }
    }
    // 描边图标四周有留白，画 24 才和文字、图片图标一样大；占位仍是 22，项目名对齐
    return SizedBox.square(
      dimension: 22,
      child: OverflowBox(
        maxWidth: 24,
        maxHeight: 24,
        child: LineIconView(LineIcon.folder, size: 24, color: c.accent),
      ),
    );
  }

  /// 项目下面的一个工作区，名字和分支照桌面侧栏：分支和名字一样时不再写一遍。
  Widget _workspaceRow(BuildContext context, WorktreeRow row) {
    final c = PosseColors.of(context);
    final title = row.isMain ? '主工作区' : row.name;
    final branch = model.settings.workspaceMetaHidden || row.branch == title
        ? ''
        : row.branch;
    return _Indented(
      indent: 34,
      child: RowTile(
        // 照桌面侧栏：主工作区是房子，git worktree 是分支，同一目录上的文件夹工作区是文件夹
        leading: LineIconView(
          row.isMain
              ? LineIcon.home
              : row.isWorktree
              ? LineIcon.branch
              : LineIcon.folder,
          size: 22,
          color: c.accent,
        ),
        title: Text(title),
        subtitle: branch.isEmpty
            ? null
            : Text(branch, style: mono(12, color: c.muted)),
        trailing: _count(c, row),
        onTap: () => _openWorkspace(context, row),
      ),
    );
  }

  /// 项目里最需要注意的会话状态。收起时显示在项目行上，待处理不会被藏起来。
  AgentState _projectState(List<WorktreeRow> rows) {
    var best = AgentState.none;
    // 照桌面 statusRank：待处理最要紧，其次是完成了还没看
    int rank(AgentState s) => switch (s) {
      AgentState.waiting => 5,
      AgentState.unread => 4,
      AgentState.working || AgentState.tasks => 3,
      AgentState.starting => 1,
      _ => 0,
    };
    for (final row in rows) {
      for (final tab in model.tabs[row.id] ?? const <SessionTab>[]) {
        final state = _stateOf(tab);
        if (rank(state) > rank(best)) best = state;
      }
    }
    return best;
  }

  Widget _where(PosseColors c, WorktreeRow row) {
    if (row.branch.isEmpty) {
      return Text(tildePath(row.path), style: mono(12, color: c.muted));
    }
    return Row(
      children: [
        LineIconView(LineIcon.branch, size: 14, color: c.muted),
        const SizedBox(width: 4),
        Flexible(
          child: Text(
            row.branch,
            overflow: TextOverflow.ellipsis,
            style: mono(12, color: c.muted),
          ),
        ),
      ],
    );
  }

  /// 右边的终端数：桌面侧栏同样位置的数字，项目收起时也知道里面有几个。
  Widget? _count(PosseColors c, WorktreeRow row) {
    if (row.live == 0) return null;
    return Text(
      '${row.live}',
      style: TextStyle(
        fontSize: 13,
        color: c.faint,
        fontFeatures: const [FontFeature.tabularFigures()],
      ),
    );
  }

  /// 会话的 agent 方块和工作区的描边图标一样大：每一级都缩进「图标 + 间距」，子级的图标对齐上一级的名字，
  /// 方块大了会显得比工作区高一级。
  static const _sessionTile = 22.0;

  List<Widget> _sessions(
    BuildContext context,
    WorktreeRow row, {
    required double indent,
  }) {
    final c = PosseColors.of(context);
    final all = model.tabs[row.id] ?? const <SessionTab>[];
    final tabs = all.where((t) => t.agent != null);
    final shells = all.where((t) => t.agent == null);
    return [
      for (final tab in tabs)
        _Indented(
          indent: indent - 14,
          child: RowTile(
            leading: AgentTile(agent: tab.agent, size: _sessionTile),
            title: Text(cleanTitle(tab.title)),
            subtitle: Text(
              model.rpc == null ? '等连上电脑' : sessionDetail(tab),
              style: TextStyle(
                color: _stateOf(tab) == AgentState.waiting ? c.honey : null,
              ),
            ),
            trailing: _sessionState(c, tab),
            onTap: () => openSession(context, model, tab),
            onLongPress: () => closeSession(context, model, tab),
          ),
        ),
      // 普通终端和 agent 会话一样各占一行，标题是标签名（桌面改过名的用改的名字）
      for (final tab in shells)
        _Indented(
          indent: indent - 14,
          child: RowTile(
            leading: const AgentTile(agent: null, size: _sessionTile),
            title: Text(cleanTitle(tab.title)),
            subtitle: Text(
              model.rpc == null
                  ? '等连上电脑'
                  : tab.posse == 'lost'
                  ? '未恢复，点开时拉起'
                  : '终端',
            ),
            onTap: () => openSession(context, model, tab),
            onLongPress: () => closeSession(context, model, tab),
          ),
        ),
    ];
  }

  /// 首页显示的会话状态。断线时手上的状态已经过时，一律不显示，等你处理的横幅、项目行和会话行一致。
  AgentState _stateOf(SessionTab tab) =>
      model.rpc == null ? AgentState.none : tab.shown;

  Widget _sessionState(PosseColors c, SessionTab tab) {
    final state = _stateOf(tab);
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        if (state == AgentState.waiting)
          Padding(
            padding: const EdgeInsets.only(right: 6),
            child: Text(
              '待处理',
              style: TextStyle(
                fontSize: 13,
                fontWeight: FontWeight.w500,
                color: c.honey,
              ),
            ),
          ),
        StatusHex(state: state),
      ],
    );
  }
}

/// 首页的项目行：点这一行展开收起下面的工作区和会话，点右边的圆按钮进项目详情。
/// 下面没有东西可展开时，点这一行也进详情。
class _ProjectHeader extends StatelessWidget {
  const _ProjectHeader({
    required this.name,
    required this.icon,
    this.subtitle,
    required this.count,
    required this.expandable,
    required this.collapsed,
    required this.state,
    required this.onToggle,
    required this.onOpen,
  });

  final String name;
  final Widget icon;

  /// 分支或路径；设置里隐藏了时为空，只留名字。
  final Widget? subtitle;
  final int count;
  final bool expandable;
  final bool collapsed;
  final AgentState state;
  final VoidCallback onToggle;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return InkWell(
      onTap: expandable ? onToggle : onOpen,
      highlightColor: c.wash,
      child: ConstrainedBox(
        constraints: const BoxConstraints(minHeight: 60),
        child: Padding(
          padding: const EdgeInsets.fromLTRB(14, 10, 10, 10),
          child: Row(
            children: [
              icon,
              const SizedBox(width: 12),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Row(
                      children: [
                        Flexible(
                          child: Text(
                            name,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: TextStyle(
                              fontSize: Fs.body,
                              fontWeight: FontWeight.w500,
                              color: c.text,
                            ),
                          ),
                        ),
                        if (expandable)
                          AnimatedRotation(
                            turns: collapsed ? -0.25 : 0,
                            // 和下面的 Fold 同时转完
                            duration: const Duration(milliseconds: 220),
                            child: Icon(
                              Icons.expand_more_rounded,
                              size: 18,
                              color: c.faint,
                            ),
                          ),
                      ],
                    ),
                    if (subtitle case final sub?) ...[
                      const SizedBox(height: 3),
                      DefaultTextStyle.merge(
                        style: TextStyle(fontSize: Fs.meta, color: c.muted),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        child: sub,
                      ),
                    ],
                  ],
                ),
              ),
              if (collapsed && hasGlyph(state)) ...[
                const SizedBox(width: 8),
                StatusHex(state: state),
              ],
              if (count > 0) ...[
                const SizedBox(width: 8),
                Text(
                  '$count',
                  style: TextStyle(
                    fontSize: 13,
                    color: c.faint,
                    fontFeatures: const [FontFeature.tabularFigures()],
                  ),
                ),
              ],
              const SizedBox(width: 8),
              // 进详情的按钮单独一块点击区，和展开收起分开
              Material(
                color: c.fill,
                shape: const CircleBorder(),
                child: InkWell(
                  customBorder: const CircleBorder(),
                  onTap: onOpen,
                  child: SizedBox.square(
                    dimension: 32,
                    child: Icon(
                      Icons.chevron_right_rounded,
                      size: 20,
                      color: c.muted,
                    ),
                  ),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

/// 子项往右缩进一截，层级一眼看得出。
class _Indented extends StatelessWidget {
  const _Indented({required this.indent, required this.child});

  final double indent;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: EdgeInsets.only(left: indent),
      child: child,
    );
  }
}

/// 有会话等你处理时在最上面提醒一句。
class _WaitingBanner extends StatelessWidget {
  const _WaitingBanner({required this.count});

  final int count;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Container(
      margin: const EdgeInsets.fromLTRB(16, 0, 16, 16),
      padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 12),
      decoration: BoxDecoration(
        color: c.honey.withValues(alpha: c.dark ? 0.14 : 0.10),
        borderRadius: BorderRadius.circular(Rd.card),
      ),
      child: Row(
        children: [
          const StatusHex(state: AgentState.waiting, size: 18),
          const SizedBox(width: 10),
          Text(
            '$count 个会话等你处理',
            style: TextStyle(
              fontSize: 14,
              fontWeight: FontWeight.w500,
              color: c.honey,
            ),
          ),
        ],
      ),
    );
  }
}

/// 首页列表上面浮着的报错（连着电脑时刷新失败之类）。断线不在这里说，电脑胶囊会写。
/// 浮在上面、不拦点击，出现和消失都不挤动列表。
class _FloatingError extends StatelessWidget {
  const _FloatingError({required this.model});

  final PosseAppModel model;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final error = model.rpc == null || model.connecting ? null : model.error;
    return IgnorePointer(
      child: Center(
        child: AnimatedSwitcher(
          duration: const Duration(milliseconds: 220),
          switchInCurve: Curves.easeOutCubic,
          switchOutCurve: Curves.easeInCubic,
          transitionBuilder: (child, animation) => FadeTransition(
            opacity: animation,
            child: SlideTransition(
              position: Tween(
                begin: const Offset(0, -0.4),
                end: Offset.zero,
              ).animate(animation),
              child: child,
            ),
          ),
          child: error == null
              ? const SizedBox.shrink(key: ValueKey('none'))
              : Container(
                  key: ValueKey(error),
                  padding: const EdgeInsets.symmetric(
                    horizontal: 14,
                    vertical: 8,
                  ),
                  decoration: BoxDecoration(
                    color: c.raised,
                    borderRadius: BorderRadius.circular(20),
                    border: Border.all(color: c.line),
                    boxShadow: [
                      BoxShadow(
                        color: Colors.black.withValues(
                          alpha: c.dark ? 0.35 : 0.12,
                        ),
                        blurRadius: 12,
                        offset: const Offset(0, 3),
                      ),
                    ],
                  ),
                  child: Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      Icon(
                        Icons.error_outline_rounded,
                        size: 16,
                        color: c.danger,
                      ),
                      const SizedBox(width: 8),
                      Flexible(
                        child: Text(
                          error,
                          maxLines: 2,
                          overflow: TextOverflow.ellipsis,
                          style: TextStyle(
                            fontSize: 13,
                            height: 1.35,
                            color: c.danger,
                          ),
                        ),
                      ),
                    ],
                  ),
                ),
        ),
      ),
    );
  }
}

/// 标题旁的电脑胶囊：连着时是电脑名；断线重连时换成琥珀色的转圈和完整的状态，胶囊跟着放大，连上后缩回去；
/// 连不上、停下等重试时是红点，和页面中间的报错一致。
class _ConnectionLine extends StatelessWidget {
  const _ConnectionLine({required this.model});

  final PosseAppModel model;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    // 主动断开的不算掉线：灰点加「已断开」，不转圈
    final off = model.disconnected && !model.connecting;
    final failed = model.failed;
    final offline = !off && !failed && (model.rpc == null || model.connecting);
    final current = model.current;
    final name = current == null ? '' : '${model.labelOf(current)} · ';
    final text = model.connecting
        ? '正在连接电脑…'
        : off
        ? '$name已断开'
        : failed
        ? '$name连不上'
        : offline
        ? '连接断开，正在重连…'
        : model.labelOf(model.current);
    return AnimatedSize(
      duration: const Duration(milliseconds: 260),
      curve: Curves.easeOutCubic,
      alignment: Alignment.centerLeft,
      child: AnimatedSwitcher(
        duration: const Duration(milliseconds: 200),
        layoutBuilder: (current, previous) => Stack(
          alignment: Alignment.centerLeft,
          children: [...previous, ?current],
        ),
        child: Row(
          key: ValueKey(text),
          mainAxisSize: MainAxisSize.min,
          children: [
            if (offline)
              HexSpinner(size: 11, color: c.honey)
            else
              Container(
                width: 7,
                height: 7,
                decoration: BoxDecoration(
                  color: off
                      ? c.faint
                      : failed
                      ? c.danger
                      : c.sage,
                  shape: BoxShape.circle,
                ),
              ),
            const SizedBox(width: 6),
            Flexible(
              child: Text(
                text,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: TextStyle(
                  fontSize: 13,
                  color: offline
                      ? c.honey
                      : failed
                      ? c.danger
                      : c.muted,
                ),
              ),
            ),
            Icon(Icons.expand_more_rounded, size: 16, color: c.faint),
          ],
        ),
      ),
    );
  }
}

/// 配对过的电脑：点一台切过去，长按改名或忘掉它，底下扫码再加一台。同一时间只连一台。
class _ComputerSheet extends StatelessWidget {
  const _ComputerSheet({
    required this.model,
    required this.onAdd,
    required this.onDesk,
  });

  final PosseAppModel model;
  final VoidCallback onAdd;
  final VoidCallback onDesk;

  /// 长按一台电脑：改名或者忘掉它。
  Future<void> _actions(BuildContext context, SavedComputer computer) async {
    final action = await showModalBottomSheet<String>(
      context: context,
      showDragHandle: true,
      builder: (sheet) {
        final c = PosseColors.of(sheet);
        return SafeArea(
          top: false,
          child: Section(
            title: model.labelOf(computer),
            inset: 48,
            children: [
              RowTile(
                chevron: false,
                leading: Icon(Icons.edit_outlined, size: 22, color: c.accent),
                title: const Text('重命名'),
                onTap: () => Navigator.of(sheet).pop('rename'),
              ),
              RowTile(
                chevron: false,
                leading: Icon(
                  Icons.link_off_rounded,
                  size: 22,
                  color: c.danger,
                ),
                title: Text('忘掉这台电脑', style: TextStyle(color: c.danger)),
                onTap: () => Navigator.of(sheet).pop('remove'),
              ),
            ],
          ),
        );
      },
    );
    if (!context.mounted) return;
    switch (action) {
      case 'rename':
        await _rename(context, computer);
      case 'remove':
        await _remove(context, computer);
    }
  }

  Future<void> _rename(BuildContext context, SavedComputer computer) async {
    final name = await showPosseDialog<String>(
      context: context,
      builder: (_) => _RenameDialog(computer: computer),
    );
    if (name != null) model.renameComputer(computer.id, name);
  }

  Future<void> _remove(BuildContext context, SavedComputer computer) async {
    final ok = await showPosseDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text('忘掉 ${model.labelOf(computer)}'),
        content: const Text('之后要重新扫码才能连上它。'),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('取消'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, true),
            child: const Text('忘掉'),
          ),
        ],
      ),
    );
    if (ok == true) await model.removeComputer(computer.id);
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) {
        final active = model.activeId;
        final list = Section(
          inset: 50,
          children: [
            if (deskSupported && model.settings.deskWatch)
              RowTile(
                leading: Icon(
                  Icons.desktop_mac_outlined,
                  size: 24,
                  color: c.accent,
                ),
                title: const Text('看桌面'),
                onTap: onDesk,
              ),
            for (final computer in model.computers)
              RowTile(
                chevron: false,
                leading: Icon(
                  Icons.laptop_mac_rounded,
                  size: 24,
                  color: c.accent,
                ),
                title: Text(model.labelOf(computer)),
                // 连着的那台写实际连上的地址，其余写配对码里的主地址
                subtitle: Text(
                  computer.id == active && model.connectedUrl != null
                      ? hostOf(model.connectedUrl) ?? ''
                      : computerEndpoint(computer),
                  style: mono(12, color: c.muted),
                ),
                trailing: computer.id == active
                    ? (model.connecting
                          ? const HexSpinner(size: 18)
                          : Icon(Icons.check_rounded, color: c.accent))
                    : null,
                onTap: () {
                  Navigator.of(context).pop();
                  model.switchTo(computer.id);
                },
                onLongPress: () => _actions(context, computer),
              ),
          ],
        );
        final footer = [
          if (model.error != null) NoticeBar(model.error!),
          // 连着或正在连时可以主动断开：配对留着，点上面的电脑或首页的重新连接再连
          if (model.rpc != null || model.connecting)
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
              child: OutlinedButton.icon(
                onPressed: () {
                  Navigator.of(context).pop();
                  unawaited(model.disconnect());
                },
                icon: const Icon(Icons.link_off_rounded, size: 20),
                label: const Text('断开连接'),
              ),
            ),
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
            child: FilledButton.icon(
              onPressed: onAdd,
              icon: const Icon(Icons.qr_code_scanner_rounded, size: 20),
              label: const Text('扫码添加电脑'),
            ),
          ),
          Padding(
            padding: const EdgeInsets.only(bottom: 8),
            child: Text(
              '长按一台电脑可以改名或忘掉它',
              textAlign: TextAlign.center,
              style: TextStyle(fontSize: 12, color: c.faint),
            ),
          ),
        ];
        return SafeArea(
          top: false,
          child: LayoutBuilder(
            builder: (context, box) {
              // 横屏只有三百多高，按钮固定的话列表会被压成 0：高度不够时整张一起滚
              if (!box.hasBoundedHeight || box.maxHeight < _sheetPinnedHeight) {
                return SingleChildScrollView(
                  child: Column(
                    mainAxisSize: MainAxisSize.min,
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [list, ...footer],
                  ),
                );
              }
              return Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Flexible(child: SingleChildScrollView(child: list)),
                  ...footer,
                ],
              );
            },
          ),
        );
      },
    );
  }
}

/// 电脑列表弹窗至少这么高才固定底下的按钮、只滚列表：按钮和提示约占 200，列表至少留两行。
const _sheetPinnedHeight = 360.0;

/// 配对码里的主地址（host:port），电脑列表的副标题。
String computerEndpoint(SavedComputer computer) {
  try {
    final uri = Uri.parse(PairingOffer.parse(computer.code).endpoint);
    return uri.hasPort ? '${uri.host}:${uri.port}' : uri.host;
  } catch (_) {
    return '';
  }
}

/// 主动断开后首页的样子：列出配对过的电脑和台数，点一台就连它；底下可以扫码添加。
class _DisconnectedList extends StatelessWidget {
  const _DisconnectedList({required this.model, required this.onAdd});

  final PosseAppModel model;
  final VoidCallback onAdd;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final computers = model.computers;
    return ListView(
      padding: EdgeInsets.only(
        top: 8,
        bottom: 16 + MediaQuery.paddingOf(context).bottom,
      ),
      children: [
        Section(
          title: '已断开 · 可以连接的电脑 ${computers.length} 台',
          inset: 50,
          children: [
            for (final computer in computers)
              RowTile(
                leading: Icon(
                  Icons.laptop_mac_rounded,
                  size: 24,
                  color: c.accent,
                ),
                title: Text(model.labelOf(computer)),
                subtitle: Text(
                  computerEndpoint(computer),
                  style: mono(12, color: c.muted),
                ),
                onTap: () => model.connectCode(computer.code),
              ),
          ],
        ),
        Center(
          child: TextButton.icon(
            onPressed: onAdd,
            icon: const Icon(Icons.qr_code_scanner_rounded, size: 20),
            label: const Text('扫码添加电脑'),
          ),
        ),
      ],
    );
  }
}

/// 工作区详情：改动和文件两页。会话都在首页工作区下面，这里不再重复。
class TabScreen extends StatefulWidget {
  const TabScreen({
    super.key,
    required this.model,
    required this.worktree,
    this.page = 0,
  });

  final PosseAppModel model;
  final WorktreeRow worktree;

  /// 打开时停在哪一页：0 改动，1 文件。
  final int page;

  @override
  State<TabScreen> createState() => _TabScreenState();
}

class _TabScreenState extends State<TabScreen> {
  late var _page = widget.page;

  /// 打开过的页。IndexedStack 会给每一页排版，没打开过的先放空盒，进页时只建看得见的那页。
  late final _seen = {widget.page};

  /// 工作区在列表里的最新一行，分支跟着首页的轮询变。
  WorktreeRow get _row {
    for (final row in widget.model.worktrees) {
      if (row.id == widget.worktree.id) return row;
    }
    return widget.worktree;
  }

  Future<T> _git<T>(Future<T> Function(PhoneRpc rpc) run) {
    final rpc = widget.model.rpc;
    if (rpc == null) return Future.error('还没连上电脑');
    return run(rpc);
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final row = _row;
    return Scaffold(
      appBar: AppBar(
        titleSpacing: 0,
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(worktreeLabel(row), overflow: TextOverflow.ellipsis),
            Text(
              row.branch.isNotEmpty ? row.branch : tildePath(row.path),
              overflow: TextOverflow.ellipsis,
              // 顶栏标题样式是 600，副标题要显式改回常规字重
              style: mono(
                12,
                color: c.muted,
              ).copyWith(fontWeight: FontWeight.w400),
            ),
          ],
        ),
      ),
      body: Column(
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 4, 16, 12),
            child: Segmented(
              labels: const ['改动', '文件'],
              selected: _page,
              onChanged: (i) => setState(() {
                _page = i;
                _seen.add(i);
              }),
            ),
          ),
          Expanded(
            child: IndexedStack(
              index: _page,
              children: [
                if (!_seen.contains(0))
                  const SizedBox.shrink()
                else
                  GitPane(
                    active: _page == 0,
                    loadStatus: () => _git((rpc) => loadGitStatus(rpc, row.id)),
                    loadDiff: (file) =>
                        _git((rpc) => loadGitDiff(rpc, row.id, file)),
                    onOpenFile: (path) => Navigator.of(context).push(
                      MaterialPageRoute<void>(
                        builder: (_) => FileViewer(
                          model: widget.model,
                          worktree: row.id,
                          path: path,
                        ),
                      ),
                    ),
                  ),
                if (!_seen.contains(1))
                  const SizedBox.shrink()
                else
                  FilesPane(model: widget.model, worktree: row.id),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

/// 给电脑改名。输入框的控制器跟着对话框走：在外面关掉对话框后马上释放，
/// 关闭动画里的输入框还会用它，就报"used after being disposed"。
class _RenameDialog extends StatefulWidget {
  const _RenameDialog({required this.computer});

  final SavedComputer computer;

  @override
  State<_RenameDialog> createState() => _RenameDialogState();
}

class _RenameDialogState extends State<_RenameDialog> {
  late final _field = TextEditingController(text: widget.computer.customName);

  @override
  void dispose() {
    _field.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final computer = widget.computer;
    return AlertDialog(
      // 系统默认两边留 40，输入框太窄
      insetPadding: const EdgeInsets.symmetric(horizontal: 20),
      title: const Text('给电脑改名'),
      content: SizedBox(
        width: double.maxFinite,
        child: TextField(
          controller: _field,
          autofocus: true,
          textInputAction: TextInputAction.done,
          onSubmitted: (v) => Navigator.pop(context, v),
          decoration: InputDecoration(
            hintText: computer.name.isEmpty ? '名字' : computer.name,
            helperText: '只改手机上显示的名字，清空就用电脑自己的名字',
            helperMaxLines: 2,
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context),
          child: const Text('取消'),
        ),
        FilledButton(
          onPressed: () => Navigator.pop(context, _field.text),
          child: const Text('保存'),
        ),
      ],
    );
  }
}
