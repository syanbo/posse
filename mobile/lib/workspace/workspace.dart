import '../discussions/discussions.dart';
import '../history/history.dart';
import '../l10n/l10n.dart';
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
import '../git/branches.dart';
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
    if (!await connectAndAnnounce(context, widget.model, code)) {
      // 已连着别的电脑时，model.error 会被那台电脑的定时刷新清掉，所以连不上的原因记在这一页上，
      // 留到下一次尝试。还在连说明被新的一次顶掉了，不算失败
      if (!mounted || widget.model.connecting) return;
      final error = widget.model.error;
      final l = context.l10n;
      setState(
        () =>
            _note = error == null ? l.homeCantConnect : friendlyError(error, l),
      );
      return;
    }
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
        setState(() => _note = scanNote(context.l10n, pick.end));
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
      _note = text.isEmpty
          ? context.l10n.pairClipboardEmpty
          : context.l10n.pairClipboardNotCode;
    });
  }

  @override
  Widget build(BuildContext context) {
    final model = widget.model;
    final c = PosseColors.of(context);
    final l = context.l10n;
    final busy = model.connecting;
    final note =
        _note ?? (model.error == null ? null : friendlyError(model.error!, l));
    return Scaffold(
      appBar: widget.adding ? AppBar() : null,
      body: SafeArea(
        child: ListView(
          padding: EdgeInsets.fromLTRB(24, widget.adding ? 8 : 32, 24, 24),
          children: [
            Align(
              alignment: Alignment.centerLeft,
              // 和桌面端、手机桌面上的 App 图标同一张图（design/icon-art.png）
              child: ClipRRect(
                borderRadius: BorderRadius.circular(Rd.card),
                child: Image.asset('assets/icon.png', width: 64, height: 64),
              ),
            ),
            const SizedBox(height: 24),
            Text(
              widget.adding ? l.pairAddTitle : l.pairConnectTitle,
              style: TextStyle(
                fontSize: Fs.hero,
                fontWeight: Fw.bold,
                color: c.text,
              ),
            ),
            const SizedBox(height: 8),
            Text(
              widget.adding ? l.pairAddNote : l.pairIntro,
              style: TextStyle(
                fontSize: Fs.body,
                height: Lh.body,
                color: c.muted,
              ),
            ),
            const SizedBox(height: 24),
            Section(
              inset: 52,
              margin: const EdgeInsets.only(bottom: 20),
              children: [
                _step(context, 1, l.pairStepOpenTitle, l.pairStepOpenDetail),
                _step(context, 2, l.pairStepScanTitle, l.pairStepScanDetail),
              ],
            ),
            if (canScan) ...[
              FilledButton.icon(
                onPressed: busy ? null : _scan,
                icon: const Icon(
                  Icons.qr_code_scanner_rounded,
                  size: Ic.medium,
                ),
                label: Text(busy ? l.pairConnecting : l.pairScanButton),
              ),
              const SizedBox(height: 10),
            ],
            TextButton.icon(
              onPressed: busy ? null : _paste,
              icon: const Icon(Icons.content_paste_rounded, size: Ic.medium),
              label: Text(l.pairPasteCode),
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
                child: Text(_manual ? l.pairHideManual : l.pairManual),
              ),
            ),
            if (_manual) ...[
              TextField(
                controller: _code,
                minLines: 3,
                maxLines: 6,
                style: mono(Fs.small, color: c.text),
                decoration: const InputDecoration(
                  hintText: 'orca://pair?code=…',
                ),
              ),
              const SizedBox(height: 10),
              FilledButton(
                onPressed: busy ? null : () => _connect(_code.text),
                child: Text(busy ? l.pairConnecting : l.pairConnect),
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
            fontSize: Fs.small,
            fontWeight: Fw.bold,
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
  final l = context.l10n;
  if (!await model.connectCode(code)) return false;
  final label = model.labelOf(model.current, l);
  final name = label.isEmpty ? l.homeComputerFallback : label;
  final where = hostOf(l, model.connectedUrl);
  messenger.showSnackBar(
    SnackBar(
      content: Text(
        where == null ? l.pairConnected(name) : l.pairConnectedAt(name, where),
      ),
    ),
  );
  return true;
}

String? scanNote(AppLocalizations l, ScanEnd end) => switch (end) {
  ScanEnd.denied => l.pairCameraDenied,
  ScanEnd.failed => l.pairCameraFailed,
  _ => null,
};

/// 地址里给人看的部分：主机加端口，路径不写。QUIC 标出来，经中转的不写中转的地址（令牌也不露）。
String? hostOf(AppLocalizations l, String? url) {
  if (url == null) return null;
  final uri = Uri.tryParse(url);
  if (uri == null || uri.host.isEmpty) return null;
  if (isRelayQuicUrl(url)) return l.homeAddressRelay;
  if (isPunchedQuicUrl(url)) return l.homeAddressDirect;
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
String sessionDetail(AppLocalizations l, SessionTab tab) {
  final tool = tab.toolName;
  return switch (tab.shown) {
    AgentState.waiting =>
      tool == null ? l.sessionDetailWaiting : l.sessionDetailApprove(tool),
    AgentState.working =>
      tool == null ? l.sessionStateWorking : l.sessionDetailUsing(tool),
    AgentState.done => l.sessionDetailDone,
    AgentState.unread => l.sessionStateUnread,
    AgentState.tasks => l.sessionStateTasks,
    AgentState.starting => l.sessionStateStarting,
    AgentState.lost => l.sessionDetailLost,
    _ =>
      tab.agent == null
          ? l.sessionTerminal
          : (tab.eligible ? l.sessionStateIdle : l.sessionDetailTerminalOnly),
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
        note = model.error == null
            ? context.l10n.homeCantConnect
            : friendlyError(model.error!, context.l10n);
      case ScanEnd.denied || ScanEnd.failed:
        note = scanNote(context.l10n, pick.end);
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
    final l = context.l10n;
    final projects = <String, List<WorktreeRow>>{};
    for (final row in model.worktrees) {
      projects
          .putIfAbsent(row.repo.isEmpty ? row.name : row.repo, () => [])
          .add(row);
    }
    final names = model.orderProjects(projects.keys.toList());
    final byStatus = model.settings.homeByStatus;
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
                  borderRadius: BorderRadius.circular(Rd.pill),
                  child: InkWell(
                    borderRadius: BorderRadius.circular(Rd.pill),
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
                            size: Ic.inline,
                          ),
                        ),
                      IconButton(
                        tooltip: l.discTitle,
                        visualDensity: VisualDensity.compact,
                        onPressed: () => Navigator.of(context).push(
                          MaterialPageRoute<void>(
                            builder: (_) => DiscussionsScreen(model: model),
                          ),
                        ),
                        // 实心双气泡比同排描边图标显大，缩小墨迹但保留图标占位和点击区域。
                        icon: const SizedBox.square(
                          dimension: Ic.button,
                          child: Center(
                            child: Icon(Icons.forum_rounded, size: Ic.medium),
                          ),
                        ),
                      ),
                      IconButton(
                        tooltip: l.homeHistoryTip,
                        visualDensity: VisualDensity.compact,
                        onPressed: () => Navigator.of(context).push(
                          MaterialPageRoute<void>(
                            builder: (_) => HistoryScreen(model: model),
                          ),
                        ),
                        // 圆形的图标墨迹比方形的小一圈，大两号才和设置看起来一样大
                        icon: Icon(
                          Icons.history_rounded,
                          size: Ic.button,
                          color: c.text,
                        ),
                      ),
                      IconButton(
                        tooltip: l.sessionNew,
                        visualDensity: VisualDensity.compact,
                        onPressed: () => showNewSession(context, model),
                        icon: Icon(
                          Icons.add_circle_outline_rounded,
                          size: Ic.button,
                          color: c.text,
                        ),
                      ),
                    ],
                  ),
          ),
          IconButton(
            tooltip: l.homeSettingsTip,
            visualDensity: VisualDensity.compact,
            onPressed: () => Navigator.of(context).push(
              MaterialPageRoute<void>(
                builder: (_) => SettingsScreen(
                  model: model,
                  onComputers: () => _showComputers(context),
                ),
              ),
            ),
            icon: LineIconView(
              LineIcon.settings,
              size: Ic.button,
              color: c.text,
            ),
          ),
          const SizedBox(width: 10),
        ],
      ),
      body: SideSafe(
        child: model.worktrees.isEmpty
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
                      text: model.error == null
                          ? l.homeNoWorkspaces
                          : friendlyError(model.error!, l),
                      error: model.error != null,
                      action: TextButton(
                        onPressed: model.retry,
                        child: Text(l.commonRetry),
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
                              Padding(
                                // 和项目卡片同宽
                                padding: const EdgeInsets.fromLTRB(
                                  10,
                                  0,
                                  10,
                                  10,
                                ),
                                child: Segmented(
                                  labels: [l.homeTabProjects, l.homeTabRunning],
                                  selected: byStatus ? 1 : 0,
                                  onChanged: (i) {
                                    // 每次切进来按当下的状态重排一次
                                    model.runOrder = null;
                                    model.updateSettings(
                                      (s) => s.homeByStatus = i == 1,
                                    );
                                  },
                                ),
                              ),
                            ],
                          ),
                        ),
                      ),
                      if (byStatus)
                        SliverToBoxAdapter(
                          child: _running(context, names, projects),
                        )
                      else
                        SliverReorderableList(
                          itemCount: names.length,
                          proxyDecorator: (child, _, animation) =>
                              AnimatedBuilder(
                                animation: animation,
                                builder: (context, child) => Transform.scale(
                                  scale:
                                      1 +
                                      0.02 *
                                          Curves.easeOut.transform(
                                            animation.value,
                                          ),
                                  child: Material(
                                    color: Colors.transparent,
                                    elevation: 8 * animation.value,
                                    shadowColor: PosseColors.lift,
                                    borderRadius: BorderRadius.circular(
                                      Rd.card,
                                    ),
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
                                  inset: _dividerInset,
                                  // 左右和卡片之间一样宽
                                  margin: const EdgeInsets.fromLTRB(
                                    10,
                                    0,
                                    10,
                                    10,
                                  ),
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
            : Text(tildePath(main.path), style: mono(Fs.small, color: c.muted)),
        count: rows.fold(0, (n, r) => n + r.live),
        expandable: true,
        collapsed: collapsed,
        state: _projectState(rows),
        onToggle: () => model.toggleProject(name),
        onOpen: () => _openWorkspace(context, main),
      ),
      for (final row in rows) _workspace(context, row),
    ];
  }

  /// 项目行首的图标，照桌面侧栏：设了文字或图片就用它，否则是文件夹。
  Widget _projectIcon(PosseColors c, WorktreeRow row) {
    if (row.iconText.isNotEmpty) {
      return SizedBox.square(
        dimension: Ic.button,
        child: FittedBox(
          fit: BoxFit.scaleDown,
          child: Text(
            row.iconText,
            maxLines: 1,
            style: TextStyle(fontSize: Ic.medium, color: c.accent, height: 1),
          ),
        ),
      );
    }
    if (row.iconImage.isNotEmpty) {
      if (model.projectIcon(row.iconImage) case final bytes?) {
        return ClipRRect(
          borderRadius: BorderRadius.circular(Rd.small),
          child: Image.memory(
            bytes,
            width: Ic.button,
            height: Ic.button,
            fit: BoxFit.contain,
            gaplessPlayback: true,
          ),
        );
      }
    }
    // 描边图标四周有留白，画大 2 才和文字、图片图标一样大；占位仍是 Ic.button，项目名对齐
    return SizedBox.square(
      dimension: Ic.button,
      child: OverflowBox(
        maxWidth: Ic.button + 2,
        maxHeight: Ic.button + 2,
        child: LineIconView(
          LineIcon.folder,
          size: Ic.button + 2,
          color: c.accent,
        ),
      ),
    );
  }

  /// 项目下面的一个工作区，名字和分支照桌面侧栏：分支和名字一样时不再写一遍。
  /// 和项目行一样点这一行展开收起下面的会话，点右边的圆按钮进工作区页。
  /// 工作区行和它的会话合成 Section 里的一行，分隔线跟着会话一起收起。
  Widget _workspace(BuildContext context, WorktreeRow row) {
    final c = PosseColors.of(context);
    final title = row.isMain ? context.l10n.workspaceMain : row.name;
    final branch = model.settings.workspaceMetaHidden || row.branch == title
        ? ''
        : row.branch;
    final sessions = _sessions(context, row, indent: 82);
    final collapsed = model.isWorkspaceCollapsed(row.id);
    // 和项目卡片 Section 画的分隔线一致
    final divider = Padding(
      padding: const EdgeInsets.only(left: _dividerInset),
      child: Divider(height: 1, thickness: 1, color: c.line),
    );
    return Column(
      children: [
        _Indented(
          indent: 34,
          child: _ProjectHeader(
            name: title,
            // 照桌面侧栏：主工作区是房子，git worktree 是分支，同一目录上的文件夹工作区是文件夹
            icon: LineIconView(
              row.isMain
                  ? LineIcon.home
                  : row.isWorktree
                  ? LineIcon.branch
                  : LineIcon.folder,
              size: Ic.button,
              color: c.accent,
            ),
            subtitle: branch.isEmpty
                ? null
                : Text(branch, style: mono(Fs.small, color: c.muted)),
            count: row.live,
            expandable: sessions.isNotEmpty,
            collapsed: collapsed,
            state: _projectState([row]),
            onToggle: () => model.toggleWorkspace(row.id),
            onOpen: () => _openWorkspace(context, row),
          ),
        ),
        Fold(
          open: !collapsed,
          child: Column(
            children: [
              for (final s in sessions) ...[divider, s],
            ],
          ),
        ),
      ],
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
      return Text(tildePath(row.path), style: mono(Fs.small, color: c.muted));
    }
    return Row(
      children: [
        LineIconView(LineIcon.branch, size: Ic.inline, color: c.muted),
        const SizedBox(width: 4),
        Flexible(
          child: Text(
            row.branch,
            overflow: TextOverflow.ellipsis,
            style: mono(Fs.small, color: c.muted),
          ),
        ),
      ],
    );
  }

  /// 会话的 agent 方块和工作区的描边图标一样大：每一级都缩进「图标 + 间距」，子级的图标对齐上一级的名字，
  /// 方块大了会显得比工作区高一级。
  static const _sessionTile = 22.0;

  /// 项目卡片里分隔线左边留出的宽度，对齐项目名。
  static const _dividerInset = 48.0;

  List<Widget> _sessions(
    BuildContext context,
    WorktreeRow row, {
    required double indent,
  }) {
    final all = model.tabs[row.id] ?? const <SessionTab>[];
    return [
      // 普通终端排在 agent 会话后面
      for (final tab in [
        ...all.where((t) => t.agent != null),
        ...all.where((t) => t.agent == null),
      ])
        _Indented(indent: indent - 14, child: _sessionRow(context, tab)),
    ];
  }

  /// 一个会话占一行。普通终端和 agent 会话一样各占一行，标题是标签名（桌面改过名的用改的名字）。
  /// where 是「运行中」里写在状态前面的项目和工作区。
  Widget _sessionRow(BuildContext context, SessionTab tab, {String? where}) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final String detail;
    if (model.rpc == null) {
      detail = l.homeWaitingConnection;
    } else if (tab.agent != null) {
      detail = sessionDetail(l, tab);
    } else {
      detail = tab.posse == 'lost' ? l.sessionDetailLost : l.sessionTerminal;
    }
    return RowTile(
      leading: AgentTile(agent: tab.agent, size: _sessionTile),
      title: Text(cleanTitle(tab.title)),
      subtitle: Text(
        where == null ? detail : '$where · $detail',
        style: TextStyle(
          color: tab.agent != null && _stateOf(tab) == AgentState.waiting
              ? c.honey
              : null,
        ),
      ),
      trailing: tab.agent == null ? null : _sessionState(context, tab),
      onTap: () => openSession(context, model, tab),
      onLongPress: () => closeSession(context, model, tab),
    );
  }

  /// 「运行中」：所有项目的 agent 会话平铺在一张卡片里，顺序见 [updateRunOrder]。
  Widget _running(
    BuildContext context,
    List<String> names,
    Map<String, List<WorktreeRow>> projects,
  ) {
    final c = PosseColors.of(context);
    final byId = <String, (SessionTab, String)>{};
    final natural = <String>[];
    for (final name in names) {
      final rows = projects[name]!;
      for (final row in rows) {
        final where = rows.length == 1
            ? name
            : '$name · ${row.isMain ? context.l10n.workspaceMain : row.name}';
        for (final tab in model.tabs[row.id] ?? const <SessionTab>[]) {
          if (tab.agent == null) continue;
          byId[tab.terminalId] = (tab, where);
          natural.add(tab.terminalId);
        }
      }
    }
    // 断线时和重连后还没拉到新列表时状态都不可信，先照项目的先后排，拉到了再按状态排
    final order =
        (model.rpc == null || model.tabsStale) && model.runOrder == null
        ? natural
        : model.runOrder = updateRunOrder(
            model.runOrder,
            natural,
            (id) => runRank(_stateOf(byId[id]!.$1)),
          );
    return Section(
      inset: _dividerInset,
      margin: const EdgeInsets.fromLTRB(10, 0, 10, 10),
      children: order.isEmpty
          ? [
              Padding(
                padding: const EdgeInsets.all(16),
                child: Text(
                  context.l10n.homeNoRunning,
                  style: TextStyle(fontSize: Fs.small, color: c.muted),
                ),
              ),
            ]
          : [
              for (final id in order)
                _sessionRow(context, byId[id]!.$1, where: byId[id]!.$2),
            ],
    );
  }

  /// 首页显示的会话状态。断线时手上的状态已经过时，一律不显示，等你处理的横幅、项目行和会话行一致。
  AgentState _stateOf(SessionTab tab) =>
      model.rpc == null ? AgentState.none : tab.shown;

  Widget _sessionState(BuildContext context, SessionTab tab) {
    final c = PosseColors.of(context);
    final state = _stateOf(tab);
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        if (state == AgentState.waiting)
          Padding(
            padding: const EdgeInsets.only(right: 6),
            child: Text(
              context.l10n.sessionStateWaiting,
              style: TextStyle(
                fontSize: Fs.small,
                fontWeight: Fw.medium,
                color: c.honey,
              ),
            ),
          ),
        StatusHex(state: state),
      ],
    );
  }
}

/// 首页的项目行（也用作有多个工作区时的工作区行）：点这一行展开收起下面的工作区和会话，点右边的圆按钮进项目详情。
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
                              fontWeight: Fw.medium,
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
                              size: Ic.medium,
                              color: c.faint,
                            ),
                          ),
                      ],
                    ),
                    if (subtitle case final sub?) ...[
                      const SizedBox(height: 2),
                      DefaultTextStyle.merge(
                        style: TextStyle(fontSize: Fs.small, color: c.muted),
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
                    fontSize: Fs.small,
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
                      size: Ic.medium,
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
      // 和下面的用量条、项目卡片同宽
      margin: const EdgeInsets.fromLTRB(10, 0, 10, 10),
      padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 12),
      decoration: BoxDecoration(
        color: c.soft(c.honey),
        borderRadius: BorderRadius.circular(Rd.card),
      ),
      child: Row(
        children: [
          const StatusHex(state: AgentState.waiting, size: Ic.medium),
          const SizedBox(width: 10),
          Text(
            context.l10n.homeWaitingBanner(count),
            style: TextStyle(
              fontSize: Fs.body,
              fontWeight: Fw.medium,
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
    final error = model.rpc == null || model.connecting || model.error == null
        ? null
        : friendlyError(model.error!, context.l10n);
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
                    borderRadius: BorderRadius.circular(Rd.pill),
                    border: Border.all(color: c.line),
                    boxShadow: [
                      BoxShadow(
                        color: c.shadow,
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
                        size: Ic.small,
                        color: c.danger,
                      ),
                      const SizedBox(width: 8),
                      Flexible(
                        child: Text(
                          error,
                          maxLines: 2,
                          overflow: TextOverflow.ellipsis,
                          style: TextStyle(
                            fontSize: Fs.small,
                            height: Lh.title,
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
    final l = context.l10n;
    final current = model.current;
    final name = current == null ? null : model.labelOf(current, l);
    final text = model.connecting
        ? l.homeConnecting
        : off
        ? (name == null ? l.homeDisconnected : l.homeDisconnectedNamed(name))
        : failed
        ? (name == null ? l.homeFailed : l.homeFailedNamed(name))
        : offline
        ? l.homeReconnecting
        : model.labelOf(model.current, l);
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
              HexSpinner(size: Ic.glyph, color: c.honey)
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
                  fontSize: Fs.small,
                  color: offline
                      ? c.honey
                      : failed
                      ? c.danger
                      : c.muted,
                ),
              ),
            ),
            Icon(Icons.expand_more_rounded, size: Ic.small, color: c.faint),
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
          child: SingleChildScrollView(
            child: Section(
              title: model.labelOf(computer, sheet.l10n),
              inset: 48,
              children: [
                RowTile(
                  chevron: false,
                  leading: Icon(
                    Icons.edit_rounded,
                    size: Ic.button,
                    color: c.accent,
                  ),
                  title: Text(context.l10n.homeRename),
                  onTap: () => Navigator.of(sheet).pop('rename'),
                ),
                RowTile(
                  chevron: false,
                  leading: Icon(
                    Icons.link_off_rounded,
                    size: Ic.button,
                    color: c.danger,
                  ),
                  title: Text(
                    context.l10n.homeForgetComputer,
                    style: TextStyle(color: c.danger),
                  ),
                  onTap: () => Navigator.of(sheet).pop('remove'),
                ),
              ],
            ),
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
    final l = context.l10n;
    final ok = await showPosseConfirm(
      context,
      title: l.homeForgetConfirm(model.labelOf(computer, l)),
      body: l.homeForgetBody,
      confirm: l.homeForget,
      tone: ConfirmTone.danger,
    );
    if (ok) await model.removeComputer(computer.id);
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) {
        final l = context.l10n;
        final active = model.activeId;
        final list = Section(
          inset: 50,
          children: [
            if (deskSupported && model.settings.deskWatch)
              RowTile(
                leading: Icon(
                  Icons.desktop_mac_rounded,
                  size: Ic.button,
                  color: c.accent,
                ),
                title: Text(l.homeRemoteDesktop),
                onTap: onDesk,
              ),
            for (final computer in model.computers)
              RowTile(
                chevron: false,
                leading: Icon(
                  Icons.laptop_mac_rounded,
                  size: Ic.button,
                  color: c.accent,
                ),
                title: Text(model.labelOf(computer, l)),
                // 连着的那台写实际连上的地址，其余写配对码里的主地址
                subtitle: Text(
                  computer.id == active && model.connectedUrl != null
                      ? hostOf(l, model.connectedUrl) ?? ''
                      : computerEndpoint(computer),
                  style: mono(Fs.small, color: c.muted),
                ),
                trailing: computer.id == active
                    ? (model.connecting
                          ? const HexSpinner(size: Ic.medium)
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
          if (model.error != null) NoticeBar(friendlyError(model.error!, l)),
          // 连着或正在连时可以主动断开：配对留着，点上面的电脑或首页的重新连接再连
          if (model.rpc != null || model.connecting)
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
              child: OutlinedButton.icon(
                onPressed: () {
                  Navigator.of(context).pop();
                  unawaited(model.disconnect());
                },
                icon: const Icon(Icons.link_off_rounded, size: Ic.medium),
                label: Text(l.homeDisconnect),
              ),
            ),
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
            child: FilledButton.icon(
              onPressed: onAdd,
              icon: const Icon(Icons.qr_code_scanner_rounded, size: Ic.medium),
              label: Text(l.homeScanToAdd),
            ),
          ),
          Padding(
            padding: const EdgeInsets.only(bottom: 8),
            child: Text(
              l.homeLongPressHint,
              textAlign: TextAlign.center,
              style: TextStyle(fontSize: Fs.small, color: c.muted),
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
          title: context.l10n.homeDisconnectedList(computers.length),
          inset: 50,
          children: [
            for (final computer in computers)
              RowTile(
                leading: Icon(
                  Icons.laptop_mac_rounded,
                  size: Ic.button,
                  color: c.accent,
                ),
                title: Text(model.labelOf(computer, context.l10n)),
                subtitle: Text(
                  computerEndpoint(computer),
                  style: mono(Fs.small, color: c.muted),
                ),
                onTap: () => model.connectCode(computer.code),
              ),
          ],
        ),
        Center(
          child: TextButton.icon(
            onPressed: onAdd,
            icon: const Icon(Icons.qr_code_scanner_rounded, size: Ic.medium),
            label: Text(context.l10n.homeScanToAdd),
          ),
        ),
      ],
    );
  }
}

/// 工作区详情：改动、文件和历史三页。会话都在首页工作区下面，这里不再重复。
class TabScreen extends StatefulWidget {
  const TabScreen({
    super.key,
    required this.model,
    required this.worktree,
    this.page = 0,
  });

  final PosseAppModel model;
  final WorktreeRow worktree;

  /// 打开时停在哪一页：0 改动，1 文件，2 历史。
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

  /// 分支在另一个工作区里检出着：换成那个工作区的改动页。
  void _openWorkspace(String id) {
    for (final w in widget.model.worktrees) {
      if (w.id != id) continue;
      Navigator.of(context).pushReplacement(
        MaterialPageRoute<void>(
          builder: (_) => TabScreen(model: widget.model, worktree: w),
        ),
      );
      return;
    }
  }

  Future<T> _git<T>(Future<T> Function(PhoneRpc rpc) run) {
    final rpc = widget.model.rpc;
    if (rpc == null) {
      return Future.error(const PhoneError(PhoneError.notConnected));
    }
    return run(rpc);
  }

  @override
  Widget build(BuildContext context) {
    final row = _row;
    final tabs = Segmented(
      labels: [
        context.l10n.workspaceTabChanges,
        context.l10n.workspaceTabFiles,
        context.l10n.workspaceTabHistory,
      ],
      selected: _page,
      onChanged: (i) => setState(() {
        _page = i;
        _seen.add(i);
      }),
    );
    // 分段放在顶栏里省一行；工作区名挪到改动页的分支卡上
    return Scaffold(
      appBar: AppBar(
        titleSpacing: 0,
        centerTitle: false,
        title: Padding(padding: const EdgeInsets.only(right: 16), child: tabs),
      ),
      body: SideSafe(
        child: Column(
          children: [
            // 顶栏里的分段上下各空 10，再空 2，和页面里别的块一样隔 12
            const SizedBox(height: 2),
            Expanded(
              child: IndexedStack(
                index: _page,
                children: [
                  if (!_seen.contains(0))
                    const SizedBox.shrink()
                  else
                    GitPane(
                      active: _page == 0,
                      title: worktreeLabel(row),
                      path: tildePath(row.path),
                      loadStatus: () =>
                          _git((rpc) => loadGitStatus(rpc, row.id)),
                      loadDiff: (file) =>
                          _git((rpc) => loadGitDiff(rpc, row.id, file)),
                      branches: GitBranchActions(
                        load: (repo) =>
                            _git((rpc) => loadGitBranches(rpc, row.id, repo)),
                        checkout: (repo, branch, force) => _git(
                          (rpc) => gitCheckout(
                            rpc,
                            row.id,
                            repo,
                            branch,
                            force: force,
                          ),
                        ),
                        workspaceName: (id) {
                          for (final w in widget.model.worktrees) {
                            if (w.id == id) return worktreeLabel(w);
                          }
                          return null;
                        },
                        openWorkspace: _openWorkspace,
                        onSwitched: () =>
                            unawaited(widget.model.refreshWorktrees()),
                      ),
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
                    FilesPane(
                      model: widget.model,
                      worktree: row.id,
                      active: _page == 1,
                    ),
                  if (!_seen.contains(2))
                    const SizedBox.shrink()
                  else
                    HistoryScreen(model: widget.model, cwd: row.path),
                ],
              ),
            ),
          ],
        ),
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
    final l = context.l10n;
    return AlertDialog(
      title: Text(l.homeRenameTitle),
      content: SizedBox(
        width: double.maxFinite,
        child: TextField(
          controller: _field,
          autofocus: true,
          textInputAction: TextInputAction.done,
          onSubmitted: (v) => Navigator.pop(context, v),
          decoration: InputDecoration(
            hintText: computer.name.isEmpty ? l.homeRenameHint : computer.name,
            helperText: l.homeRenameHelper,
            helperMaxLines: 2,
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context),
          child: Text(l.commonCancel),
        ),
        FilledButton(
          onPressed: () => Navigator.pop(context, _field.text),
          child: Text(l.commonSave),
        ),
      ],
    );
  }
}

/// 「运行中」的排序：要你处理的在前，空闲在后，还没恢复的最后。
int runRank(AgentState state) => switch (state) {
  AgentState.waiting => 5,
  AgentState.unread => 4,
  AgentState.working || AgentState.tasks => 3,
  AgentState.starting => 2,
  AgentState.lost => 0,
  _ => 1,
};

/// 「运行中」的新顺序：prev 为空时整列按状态排（同状态照 natural 的先后）；否则去掉没了的会话，
/// 新出现的插到同状态那一段末尾，已有的不挪，状态变了只换图形，列表不跟着状态来回跳。
List<String> updateRunOrder(
  List<String>? prev,
  List<String> natural,
  int Function(String id) rank,
) {
  if (prev == null) {
    final out = List.of(natural);
    // List.sort 不保证稳定，按 natural 的下标兜底
    final at = {for (var i = 0; i < natural.length; i++) natural[i]: i};
    out.sort((x, y) {
      final d = rank(y) - rank(x);
      return d != 0 ? d : at[x]! - at[y]!;
    });
    return out;
  }
  final live = natural.toSet();
  final out = prev.where(live.contains).toList();
  final known = out.toSet();
  for (final id in natural) {
    if (known.contains(id)) continue;
    final r = rank(id);
    // 排在最后一个状态不比它低的后面：已有的降了档还留在前面，不能找「第一个比它低的」
    out.insert(out.lastIndexWhere((x) => rank(x) >= r) + 1, id);
  }
  return out;
}
