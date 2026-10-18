import '../history/history.dart';

import 'dart:async';
import 'dart:io';

import 'package:flutter/cupertino.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../app_state.dart';
import '../chat/chat.dart';
import '../files/files.dart';
import '../git/git.dart';
import '../pair/scan.dart';
import '../protocol/pairing.dart';
import '../protocol/rpc.dart';
import '../settings/settings.dart';
import '../terminal/terminal.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'models.dart';
import 'new_session.dart';
import 'session_actions.dart';

class PairScreen extends StatefulWidget {
  const PairScreen({super.key, required this.model, this.adding = false});

  final HiveAppModel model;

  /// 已经连着一台电脑，再加一台：有返回按钮，连上新电脑后自动关掉。
  final bool adding;

  @override
  State<PairScreen> createState() => _PairScreenState();
}

class _PairScreenState extends State<PairScreen> {
  final _code = TextEditingController();
  String? _note;
  var _manual = false;
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
    final model = widget.model;
    final messenger = ScaffoldMessenger.of(context);
    final nav = Navigator.of(context);
    if (!await model.connectCode(code)) return;
    final name = model.current?.label ?? '电脑';
    final where = hostOf(model.connectedUrl);
    messenger.showSnackBar(
      SnackBar(
        content: Text(where == null ? '已连上 $name' : '已连上 $name，地址 $where'),
      ),
    );
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
    bool granted = false;
    try {
      granted = await qrChannel.invokeMethod<bool>('authorize') ?? false;
    } on MissingPluginException {
      return;
    } on PlatformException {
      if (!mounted) return;
      setState(() => _note = '打不开相机，可以把配对码贴进来。');
      return;
    }
    if (!mounted) return;
    if (!granted) {
      setState(() => _note = '需要相机权限才能扫码，也可以把配对码贴进来。');
      return;
    }
    final pick = await Navigator.of(context)
        .push<ScanPick>(MaterialPageRoute(builder: (_) => const QrScanPage()));
    if (!mounted || pick == null) return;
    if (pick.failed || pick.code == null) {
      setState(() => _note = '打不开相机，可以把配对码贴进来。');
      return;
    }
    await _connect(pick.code!);
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
    final c = HiveColors.of(context);
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
              widget.adding ? '添加电脑' : '连接 Hive',
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
                _step(context, 1, '在电脑上打开 Hive', '点窗口左下角的手机图标'),
                _step(context, 2, '扫描二维码', '或者在电脑上复制配对码，回来粘贴'),
              ],
            ),
            if (Platform.isIOS) ...[
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
    final c = HiveColors.of(context);
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

/// 地址里给人看的部分：主机加端口，路径不写。
String? hostOf(String? url) {
  if (url == null) return null;
  final uri = Uri.tryParse(url);
  if (uri == null || uri.host.isEmpty) return null;
  return uri.hasPort ? '${uri.host}:${uri.port}' : uri.host;
}

/// 工作区在列表里的名字：主工作区和项目同名时只写项目名。
String worktreeLabel(WorktreeRow row) {
  if (row.repo.isEmpty || row.name == row.repo) return row.name;
  return '${row.repo} / ${row.name}';
}

/// 打开一个会话：有原生存档的 agent 进对话页（右上角能切到终端），其余直接进终端。
void openSession(BuildContext context, HiveAppModel model, SessionTab tab) {
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
  return switch (agentStateOf(tab.state)) {
    AgentState.waiting =>
      tab.toolName == null ? '等你处理' : '等你批准 ${tab.toolName}',
    AgentState.working => tab.toolName == null ? '工作中' : '正在用 ${tab.toolName}',
    AgentState.done => '本轮完成',
    _ => tab.agent == null ? '终端' : (tab.eligible ? '空闲' : '只能看终端'),
  };
}

/// 首页照桌面端侧栏：项目 → 工作区（只有主工作区时省掉这层）→ 运行中的 agent 会话，都直接展开。
/// shell 终端在会话下面合成一行"终端 N"，不和 agent 混在一起。
class WorkspaceScreen extends StatelessWidget {
  const WorkspaceScreen({super.key, required this.model});

  final HiveAppModel model;

  void _addComputer(BuildContext context) {
    Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => PairScreen(model: model, adding: true),
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
    final c = HiveColors.of(context);
    final projects = <String, List<WorktreeRow>>{};
    for (final row in model.worktrees) {
      projects
          .putIfAbsent(row.repo.isEmpty ? row.name : row.repo, () => [])
          .add(row);
    }
    final names = model.orderProjects(projects.keys.toList());
    var waiting = 0;
    for (final list in model.tabs.values) {
      waiting += list
          .where((t) => agentStateOf(t.state) == AgentState.waiting)
          .length;
    }
    return Scaffold(
      appBar: AppBar(
        toolbarHeight: 60,
        titleSpacing: 20,
        title: Row(
          children: [
            Text(
              'Hive',
              style: TextStyle(
                fontSize: 26,
                fontWeight: FontWeight.w600,
                color: c.text,
              ),
            ),
            const SizedBox(width: 12),
            Flexible(
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
          ],
        ),
        actions: [
          // 刷新靠下拉，切换和忘掉电脑在标题旁的电脑胶囊里；这里只留扫码添加电脑
          IconButton(
            tooltip: '历史',
            onPressed: model.rpc == null
                ? null
                : () => Navigator.of(context).push(
                    MaterialPageRoute<void>(
                      builder: (_) => HistoryScreen(model: model),
                    ),
                  ),
            icon: Icon(Icons.history_rounded, size: 22, color: c.text),
          ),
          IconButton(
            tooltip: '新建会话',
            onPressed: model.rpc == null
                ? null
                : () => showNewSession(context, model),
            icon: Icon(
              Icons.add_circle_outline_rounded,
              size: 22,
              color: c.text,
            ),
          ),
          IconButton(
            tooltip: '扫码添加电脑',
            onPressed: () => _addComputer(context),
            icon: Icon(Icons.qr_code_scanner_rounded, size: 22, color: c.text),
          ),
          IconButton(
            tooltip: '设置',
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
          const SizedBox(width: 8),
        ],
      ),
      body: model.worktrees.isEmpty
          ? (model.connecting
                ? const Center(child: CircularProgressIndicator())
                : EmptyState(
                    icon: model.error == null
                        ? Icons.folder_open_rounded
                        : Icons.cloud_off_rounded,
                    text: model.error ?? '电脑上的 Hive 还没有工作区',
                    error: model.error != null,
                    action: TextButton(
                      onPressed: model.retry,
                      child: const Text('重试'),
                    ),
                  ))
          // iOS 原生的下拉刷新；长按项目卡片拖动排序，顺序按电脑分别存在手机上
          : CustomScrollView(
              physics: const BouncingScrollPhysics(
                parent: AlwaysScrollableScrollPhysics(),
              ),
              slivers: [
                CupertinoSliverRefreshControl(
                  onRefresh: model.refreshWorktrees,
                ),
                SliverToBoxAdapter(
                  child: Padding(
                    padding: const EdgeInsets.only(top: 8),
                    child: Column(
                      children: [
                        if (model.error != null) NoticeBar(model.error!),
                        if (waiting > 0) _WaitingBanner(count: waiting),
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
                          1 + 0.02 * Curves.easeOut.transform(animation.value),
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
    );
  }

  List<Widget> _project(
    BuildContext context,
    String name,
    List<WorktreeRow> rows,
  ) {
    final c = HiveColors.of(context);
    final collapsed = model.isCollapsed(name);
    // 只有主工作区：项目这一行就是工作区，会话直接挂在下面
    if (rows.length == 1) {
      final row = rows.single;
      final sessions = _sessions(context, row, indent: 48);
      return [
        _ProjectHeader(
          name: name,
          subtitle: _where(c, row),
          count: row.live,
          expandable: sessions.isNotEmpty,
          collapsed: collapsed,
          state: _projectState(rows),
          onToggle: () => model.toggleProject(name),
          onOpen: () => _openWorkspace(context, row),
        ),
        if (!collapsed) ...sessions,
      ];
    }
    final main = rows.firstWhere((r) => r.isMain, orElse: () => rows.first);
    return [
      _ProjectHeader(
        name: name,
        subtitle: Text(tildePath(main.path), style: mono(12, color: c.muted)),
        count: rows.fold(0, (n, r) => n + r.live),
        expandable: true,
        collapsed: collapsed,
        state: _projectState(rows),
        onToggle: () => model.toggleProject(name),
        onOpen: () => _openWorkspace(context, main),
      ),
      if (!collapsed)
        for (final row in rows) ...[
          _Indented(
            indent: 34,
            child: RowTile(
              // 主工作区、同一目录上的文件夹工作区、git worktree 各用各的图标
              leading: Icon(
                row.isMain
                    ? Icons.home_outlined
                    : row.branch.isEmpty
                    ? Icons.folder_copy_outlined
                    : Icons.call_split_rounded,
                // folder_copy 的墨迹比另外两个满，同样字号看着大一圈
                size: row.isMain || row.branch.isNotEmpty ? 20 : 17,
                color: c.accent,
              ),
              title: Text(row.isMain ? '主工作区' : row.name),
              subtitle: row.branch.isEmpty
                  ? null
                  : Text(row.branch, style: mono(12, color: c.muted)),
              trailing: _count(c, row),
              onTap: () => _openWorkspace(context, row),
            ),
          ),
          ..._sessions(context, row, indent: 82),
        ],
    ];
  }

  /// 项目里最需要注意的会话状态。收起时显示在项目行上，待处理不会被藏起来。
  AgentState _projectState(List<WorktreeRow> rows) {
    var best = AgentState.none;
    int rank(AgentState s) => switch (s) {
      AgentState.waiting => 3,
      AgentState.working => 2,
      AgentState.done => 1,
      _ => 0,
    };
    for (final row in rows) {
      for (final tab in model.tabs[row.id] ?? const <SessionTab>[]) {
        final state = agentStateOf(tab.state);
        if (rank(state) > rank(best)) best = state;
      }
    }
    return best;
  }

  Widget _where(HiveColors c, WorktreeRow row) {
    if (row.branch.isEmpty) {
      return Text(tildePath(row.path), style: mono(12, color: c.muted));
    }
    return Row(
      children: [
        Icon(Icons.call_split_rounded, size: 13, color: c.muted),
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

  /// 右边的终端数：桌面侧栏同样位置的数字。shell 不展开，数一下让人知道里面还有东西。
  Widget? _count(HiveColors c, WorktreeRow row) {
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

  List<Widget> _sessions(
    BuildContext context,
    WorktreeRow row, {
    required double indent,
  }) {
    final c = HiveColors.of(context);
    final all = model.tabs[row.id] ?? const <SessionTab>[];
    final tabs = all.where((t) => t.agent != null);
    final shells = all.where((t) => t.agent == null).toList();
    return [
      for (final tab in tabs)
        _Indented(
          indent: indent - 14,
          child: RowTile(
            leading: AgentTile(agent: tab.agent, size: 28),
            title: Text(cleanTitle(tab.title)),
            subtitle: Text(
              model.rpc == null ? '等连上电脑' : sessionDetail(tab),
              style: TextStyle(
                color: agentStateOf(tab.state) == AgentState.waiting
                    ? c.honey
                    : null,
              ),
            ),
            trailing: _sessionState(c, tab),
            onTap: () => openSession(context, model, tab),
            onLongPress: () => closeSession(context, model, tab),
          ),
        ),
      if (shells.isNotEmpty)
        _Indented(
          indent: indent - 14,
          child: RowTile(
            leading: const AgentTile(agent: null, size: 28),
            title: Text('终端 ${shells.length}'),
            subtitle: Text(
              shells.map((t) => cleanTitle(t.title)).join('  '),
              maxLines: 1,
            ),
            onTap: () => shells.length == 1
                ? openSession(context, model, shells.single)
                : _showShells(context, shells),
            onLongPress: shells.length == 1
                ? () => closeSession(context, model, shells.single)
                : () => _showShells(context, shells),
          ),
        ),
    ];
  }

  /// 工作区里有几个 shell 时先列出来挑一个。
  void _showShells(BuildContext context, List<SessionTab> shells) {
    showModalBottomSheet<void>(
      context: context,
      showDragHandle: true,
      builder: (sheet) => SafeArea(
        top: false,
        child: Section(
          title: '终端',
          inset: 64,
          children: [
            for (final tab in shells)
              RowTile(
                leading: const AgentTile(agent: null, size: 32),
                title: Text(cleanTitle(tab.title)),
                onTap: () {
                  Navigator.of(sheet).pop();
                  openSession(context, model, tab);
                },
                onLongPress: () async {
                  Navigator.of(sheet).pop();
                  await closeSession(context, model, tab);
                },
              ),
          ],
        ),
      ),
    );
  }

  Widget _sessionState(HiveColors c, SessionTab tab) {
    // 断线时手上的状态已经过时，不显示
    final state = model.rpc == null ? AgentState.none : agentStateOf(tab.state);
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
    required this.subtitle,
    required this.count,
    required this.expandable,
    required this.collapsed,
    required this.state,
    required this.onToggle,
    required this.onOpen,
  });

  final String name;
  final Widget subtitle;
  final int count;
  final bool expandable;
  final bool collapsed;
  final AgentState state;
  final VoidCallback onToggle;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    return InkWell(
      onTap: expandable ? onToggle : onOpen,
      highlightColor: c.wash,
      child: ConstrainedBox(
        constraints: const BoxConstraints(minHeight: 60),
        child: Padding(
          padding: const EdgeInsets.fromLTRB(14, 10, 10, 10),
          child: Row(
            children: [
              Icon(Icons.folder_outlined, size: 22, color: c.accent),
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
                            duration: const Duration(milliseconds: 150),
                            child: Icon(
                              Icons.expand_more_rounded,
                              size: 18,
                              color: c.faint,
                            ),
                          ),
                      ],
                    ),
                    const SizedBox(height: 3),
                    DefaultTextStyle.merge(
                      style: TextStyle(fontSize: Fs.meta, color: c.muted),
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      child: subtitle,
                    ),
                  ],
                ),
              ),
              if (collapsed && state != AgentState.none) ...[
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
    final c = HiveColors.of(context);
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

class _ConnectionLine extends StatelessWidget {
  const _ConnectionLine({required this.model});

  final HiveAppModel model;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final online = model.rpc != null;
    final name = model.current?.label ?? '';
    final text = model.connecting
        ? '正在连接…'
        : online
        ? name
        : '连接断开，正在重试';
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        Container(
          width: 7,
          height: 7,
          decoration: BoxDecoration(
            color: online && !model.connecting ? c.sage : c.honey,
            shape: BoxShape.circle,
          ),
        ),
        const SizedBox(width: 6),
        Flexible(
          child: Text(
            text,
            overflow: TextOverflow.ellipsis,
            style: TextStyle(fontSize: 13, color: c.muted),
          ),
        ),
        Icon(Icons.expand_more_rounded, size: 16, color: c.faint),
      ],
    );
  }
}

/// 配对过的电脑：点一台切过去，长按改名或忘掉它，底下扫码再加一台。同一时间只连一台。
class _ComputerSheet extends StatelessWidget {
  const _ComputerSheet({required this.model, required this.onAdd});

  final HiveAppModel model;
  final VoidCallback onAdd;

  /// 长按一台电脑：改名或者忘掉它。
  Future<void> _actions(BuildContext context, SavedComputer computer) async {
    final action = await showModalBottomSheet<String>(
      context: context,
      showDragHandle: true,
      builder: (sheet) {
        final c = HiveColors.of(sheet);
        return SafeArea(
          top: false,
          child: Section(
            title: computer.label,
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
    final name = await showDialog<String>(
      context: context,
      builder: (_) => _RenameDialog(computer: computer),
    );
    if (name != null) model.renameComputer(computer.id, name);
  }

  Future<void> _remove(BuildContext context, SavedComputer computer) async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text('忘掉 ${computer.label}'),
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
    final c = HiveColors.of(context);
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) {
        final active = model.offer?.publicKeyB64;
        return SafeArea(
          top: false,
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(20, 0, 20, 12),
                child: Text(
                  '电脑',
                  style: TextStyle(
                    fontSize: 20,
                    fontWeight: FontWeight.w600,
                    color: c.text,
                  ),
                ),
              ),
              Flexible(
                child: SingleChildScrollView(
                  child: Section(
                    inset: 50,
                    children: [
                      for (final computer in model.computers)
                        RowTile(
                          chevron: false,
                          leading: Icon(
                            Icons.laptop_mac_rounded,
                            size: 24,
                            color: c.accent,
                          ),
                          title: Text(computer.label),
                          // 连着的那台写实际连上的地址，其余写配对码里的主地址
                          subtitle: Text(
                            computer.id == active && model.connectedUrl != null
                                ? hostOf(model.connectedUrl) ?? ''
                                : _endpoint(computer),
                            style: mono(12, color: c.muted),
                          ),
                          trailing: computer.id == active
                              ? (model.connecting
                                    ? const SizedBox.square(
                                        dimension: 18,
                                        child: CircularProgressIndicator(
                                          strokeWidth: 2,
                                        ),
                                      )
                                    : Icon(
                                        Icons.check_rounded,
                                        color: c.accent,
                                      ))
                              : null,
                          onTap: () {
                            Navigator.of(context).pop();
                            model.switchTo(computer.id);
                          },
                          onLongPress: () => _actions(context, computer),
                        ),
                    ],
                  ),
                ),
              ),
              if (model.error != null) NoticeBar(model.error!),
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
            ],
          ),
        );
      },
    );
  }

  String _endpoint(SavedComputer computer) {
    try {
      final uri = Uri.parse(PairingOffer.parse(computer.code).endpoint);
      return uri.hasPort ? '${uri.host}:${uri.port}' : uri.host;
    } catch (_) {
      return '';
    }
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

  final HiveAppModel model;
  final WorktreeRow worktree;

  /// 打开时停在哪一页：0 改动，1 文件。
  final int page;

  @override
  State<TabScreen> createState() => _TabScreenState();
}

class _TabScreenState extends State<TabScreen> {
  late var _page = widget.page;

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
    final c = HiveColors.of(context);
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
        actions: [
          IconButton(
            tooltip: '在这个工作区新建会话',
            onPressed: widget.model.rpc == null
                ? null
                : () => showNewSession(context, widget.model, worktree: row),
            icon: const Icon(Icons.add_circle_outline_rounded, size: 22),
          ),
          const SizedBox(width: 4),
        ],
      ),
      body: Column(
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 4, 16, 12),
            child: Segmented(
              labels: const ['改动', '文件'],
              selected: _page,
              onChanged: (i) => setState(() => _page = i),
            ),
          ),
          Expanded(
            child: IndexedStack(
              index: _page,
              children: [
                GitPane(
                  loadStatus: () => _git((rpc) => loadGitStatus(rpc, row.id)),
                  loadDiff: (path) =>
                      _git((rpc) => loadGitDiff(rpc, row.id, path)),
                ),
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
