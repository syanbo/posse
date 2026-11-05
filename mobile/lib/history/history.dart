import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../app_state.dart';
import '../chat/anchor.dart';
import '../chat/chat.dart';
import '../chat/merge.dart';
import '../chat/transcript.dart';
import '../l10n/l10n.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../workspace/models.dart';
import '../workspace/workspace.dart';
import 'models.dart';

/// 电脑上的会话历史：顶上搜索和按 agent 筛选，下面按时间或文件夹分组。照桌面的历史页，只读；点开看对话，
/// 底部"在电脑上继续"让电脑在这个会话的目录里开终端接着聊。
class HistoryScreen extends StatefulWidget {
  const HistoryScreen({super.key, required this.model, this.cwd});

  final PosseAppModel model;

  /// 只列这个目录（含子目录）里的会话。给了时是项目详情里的一页：没有顶栏，搜索框在列表上面，
  /// 只按时间分组。
  final String? cwd;

  @override
  State<HistoryScreen> createState() => _HistoryScreenState();
}

class _HistoryScreenState extends State<HistoryScreen> {
  final _query = TextEditingController();
  Timer? _debounce;
  List<HistorySession> _sessions = const [];
  var _loading = true;
  String? _error;

  /// 搜索没有同时含所有词的结果，列的是含任一词的。
  var _any = false;

  /// 每次查询加一，只认最后一次的结果。
  var _seq = 0;

  /// 选中的 agent 筛选，下标对应 [historyAgents]。不记住，和桌面一样每次从全部开始。
  var _agent = 0;

  /// 用户点过的分组展开状态，按组名记；没点过的只展开第一组。换筛选或分组方式时清空。
  final _open = <String, bool>{};

  @override
  void initState() {
    super.initState();
    unawaited(_load());
  }

  @override
  void dispose() {
    _debounce?.cancel();
    _query.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    final seq = ++_seq;
    final rpc = widget.model.rpc;
    if (rpc == null) {
      setState(() {
        _loading = false;
        _error = context.l10n.errorNotConnected;
      });
      return;
    }
    setState(() => _loading = true);
    final q = _query.text.trim();
    final agent = historyAgents[_agent];
    final cwd = widget.cwd;
    try {
      final result = q.isEmpty
          ? await rpc.call('history.list', {
              'agent': agent,
              'cwd': ?cwd,
              'limit': 300,
            })
          : await rpc.call('history.search', {
              'query': q,
              'agent': agent,
              'cwd': ?cwd,
              'limit': 100,
            });
      if (!mounted || seq != _seq) return;
      await routeSettled(context);
      if (!mounted || seq != _seq) return;
      setState(() {
        _sessions = parseHistory(result);
        _any = result['any'] == true;
        _error = null;
        _loading = false;
      });
    } catch (e) {
      if (!mounted || seq != _seq) return;
      setState(() {
        _loading = false;
        _error = e is Exception && '$e'.contains('method_not_found')
            ? context.l10n.historyOldPosse
            : friendlyError(e, context.l10n);
      });
    }
  }

  void _onQuery(String _) {
    _debounce?.cancel();
    _debounce = Timer(const Duration(milliseconds: 300), _load);
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final now = DateTime.now();
    final searching = _query.text.trim().isNotEmpty;
    // 项目详情里的会话都在同一个目录，不按文件夹分
    final byFolder =
        widget.cwd == null && widget.model.settings.historyByFolder;
    final groups = byFolder
        ? groupHistoryByFolder(context.l10n, _sessions, [
            for (final w in widget.model.worktrees) (w.path, w.repo),
          ])
        : groupHistory(context.l10n, _sessions, now);
    final agent = historyAgents[_agent];
    if (widget.cwd != null) {
      return Column(
        children: [
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 16),
            child: SizedBox(height: 38, child: _searchField(c, searching)),
          ),
          Expanded(child: _body(c, searching, groups, agent, now)),
        ],
      );
    }
    return Scaffold(
      // 顶栏：返回、搜索框、分组方式。筛选只留一行 agent 小标签，不用两条整宽的分段
      appBar: AppBar(
        titleSpacing: 0,
        // 高度钉死 38：清除按钮、转圈出现时框不变高
        title: SizedBox(height: 38, child: _searchField(c, searching)),
        actions: [
          // 和桌面一样：分组方式记住；图标是现在的分组，点一下换另一种。搜索结果按相关度排、不分组
          IconButton(
            tooltip: byFolder
                ? context.l10n.historyByFolderTip
                : context.l10n.historyByTimeTip,
            onPressed: searching
                ? null
                : () {
                    widget.model.updateSettings(
                      (s) => s.historyByFolder = !byFolder,
                    );
                    setState(_open.clear);
                  },
            icon: Icon(
              byFolder ? Icons.folder_rounded : Icons.schedule_rounded,
              size: Ic.button,
              color: searching ? c.faint : c.text,
            ),
          ),
          const SizedBox(width: 6),
        ],
      ),
      body: SideSafe(child: _body(c, searching, groups, agent, now)),
    );
  }

  Widget _searchField(PosseColors c, bool searching) {
    return TextField(
      controller: _query,
      onChanged: _onQuery,
      textInputAction: TextInputAction.search,
      onSubmitted: (_) => _load(),
      // expands 要求多行，粘贴进来的换行去掉，框里始终一行
      expands: true,
      maxLines: null,
      inputFormatters: [FilteringTextInputFormatter.deny('\n')],
      textAlignVertical: TextAlignVertical.center,
      style: TextStyle(fontSize: Fs.body, color: c.text),
      decoration: InputDecoration(
        isDense: true,
        contentPadding: EdgeInsets.zero,
        hintText: context.l10n.historySearchHint,
        prefixIcon: Icon(Icons.search_rounded, size: Ic.medium, color: c.muted),
        prefixIconConstraints: const BoxConstraints.tightFor(
          width: 38,
          height: 38,
        ),
        suffixIconConstraints: const BoxConstraints.tightFor(
          width: 38,
          height: 38,
        ),
        // 已有结果时重读，转圈放在搜索框里，不挤动下面的列表
        suffixIcon: _loading && _sessions.isNotEmpty
            ? const Center(widthFactor: 1, child: HexSpinner())
            : searching
            ? IconButton(
                tooltip: context.l10n.historyClearSearch,
                padding: EdgeInsets.zero,
                icon: Icon(
                  Icons.cancel_rounded,
                  size: Ic.medium,
                  color: c.faint,
                ),
                onPressed: () {
                  _query.clear();
                  unawaited(_load());
                },
              )
            : null,
      ),
    );
  }

  Widget _body(
    PosseColors c,
    bool searching,
    List<(String, List<HistorySession>)> groups,
    String agent,
    DateTime now,
  ) {
    return Column(
      children: [
        // 写得下时铺满一行、两端对齐，窄屏写不下再横着滑
        Padding(
          // 工作区页里和分段、搜索框之间隔 12；下面的组名行自带 6 的上内边距，合起来也是 12
          padding: EdgeInsets.fromLTRB(0, widget.cwd == null ? 4 : 12, 0, 6),
          child: LayoutBuilder(
            builder: (context, box) => SingleChildScrollView(
              scrollDirection: Axis.horizontal,
              // 和下面的卡片同宽：「全部」的底色左边贴卡片左边
              padding: const EdgeInsets.symmetric(horizontal: 16),
              child: ConstrainedBox(
                constraints: BoxConstraints(minWidth: box.maxWidth - 32),
                child: Row(
                  mainAxisAlignment: MainAxisAlignment.spaceBetween,
                  children: [
                    for (var i = 0; i < historyAgents.length; i++)
                      _AgentChip(
                        agent: historyAgents[i],
                        selected: i == _agent,
                        onTap: () {
                          if (i == _agent) return;
                          _debounce?.cancel();
                          setState(() {
                            _agent = i;
                            _open.clear();
                          });
                          unawaited(_load());
                        },
                      ),
                  ],
                ),
              ),
            ),
          ),
        ),
        if (_error != null) NoticeBar(_error!),
        if (searching && _any && _sessions.isNotEmpty)
          NoticeBar(context.l10n.historyAnyWordNotice),
        Expanded(
          child: _loading && _sessions.isEmpty
              ? const Center(child: PosseLoader())
              : _sessions.isEmpty && _error == null
              ? EmptyState(
                  icon: Icons.history_rounded,
                  text: searching
                      ? context.l10n.historyNoResults
                      : switch ((widget.cwd == null, agent.isEmpty)) {
                          (true, true) => context.l10n.historyEmptyComputer,
                          (true, false) =>
                            context.l10n.historyEmptyComputerAgent(
                              agentLabel(agent),
                            ),
                          (false, true) => context.l10n.historyEmptyFolder,
                          (false, false) =>
                            context.l10n.historyEmptyFolderAgent(
                              agentLabel(agent),
                            ),
                        },
                )
              : CustomScrollView(
                  physics: const BouncingScrollPhysics(
                    parent: AlwaysScrollableScrollPhysics(),
                  ),
                  slivers: [
                    // 搜索结果按相关度排，不分组
                    if (searching)
                      SliverSection(
                        inset: 52,
                        itemCount: _sessions.length,
                        itemBuilder: (context, i) =>
                            _row(context, _sessions[i], now),
                      )
                    else
                      for (final (i, (label, list)) in groups.indexed)
                        ..._group(context, label, list, i == 0, now),
                    const SliverToBoxAdapter(child: SizedBox(height: 24)),
                  ],
                ),
        ),
      ],
    );
  }

  /// 一组：可点的组名（条数和箭头），展开时下面是卡片。
  List<Widget> _group(
    BuildContext context,
    String label,
    List<HistorySession> list,
    bool first,
    DateTime now,
  ) {
    final c = PosseColors.of(context);
    final open = _open[label] ?? first;
    return [
      SliverPadding(
        padding: EdgeInsets.fromLTRB(16, 0, 16, open ? 0 : 12),
        sliver: SliverToBoxAdapter(
          child: InkWell(
            borderRadius: BorderRadius.circular(Rd.button),
            onTap: () => setState(() => _open[label] = !open),
            child: Padding(
              padding: const EdgeInsets.fromLTRB(4, 6, 2, 8),
              child: Row(
                children: [
                  Expanded(
                    child: Text(
                      label,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: TextStyle(
                        fontSize: Fs.small,
                        fontWeight: Fw.medium,
                        color: c.muted,
                      ),
                    ),
                  ),
                  Text(
                    '${list.length}',
                    style: TextStyle(
                      fontSize: Fs.small,
                      color: c.faint,
                      fontFeatures: const [FontFeature.tabularFigures()],
                    ),
                  ),
                  const SizedBox(width: 2),
                  AnimatedRotation(
                    turns: open ? 0.25 : 0,
                    duration: const Duration(milliseconds: 150),
                    child: Icon(
                      Icons.chevron_right_rounded,
                      size: Ic.medium,
                      color: c.faint,
                    ),
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
      if (open)
        SliverSection(
          inset: 52,
          itemCount: list.length,
          itemBuilder: (context, i) => _row(context, list[i], now),
        ),
    ];
  }

  Widget _row(BuildContext context, HistorySession s, DateTime now) {
    final c = PosseColors.of(context);
    final where = [
      if (s.folder.isNotEmpty) s.folder,
      if (s.branch.isNotEmpty) s.branch,
    ].join(' · ');
    return RowTile(
      chevron: false,
      leading: AgentTile(agent: s.agent, size: Ic.large),
      title: Text(
        s.title.isEmpty ? context.l10n.historyUntitled : s.title,
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
      ),
      subtitle: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (s.snippet != null)
            Padding(
              padding: const EdgeInsets.only(bottom: 2),
              child: Text(
                s.snippet!,
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: TextStyle(
                  fontSize: Fs.small,
                  color: c.text,
                  height: Lh.code,
                ),
              ),
            ),
          Text(
            where,
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: TextStyle(fontSize: Fs.small, color: c.muted),
          ),
        ],
      ),
      // 时间放在右边和箭头一起上下居中，不跟着副标题沉到底
      trailing: Text(
        historyTime(context.l10n, s.updatedAt, now, h24: use24Hour(context)),
        style: TextStyle(
          fontSize: Fs.small,
          color: c.faint,
          fontFeatures: const [FontFeature.tabularFigures()],
        ),
      ),
      onTap: () => Navigator.of(context).push(
        MaterialPageRoute<void>(
          builder: (_) => HistorySessionScreen(model: widget.model, session: s),
        ),
      ),
    );
  }
}

/// 一个历史会话的对话，只读。列表和对话页一样（往上翻自动读更早的），只有底部换成"在电脑上继续"。
class HistorySessionScreen extends StatefulWidget {
  const HistorySessionScreen({
    super.key,
    required this.model,
    required this.session,
  });

  final PosseAppModel model;
  final HistorySession session;

  @override
  State<HistorySessionScreen> createState() => _HistorySessionScreenState();
}

class _HistorySessionScreenState extends State<HistorySessionScreen> {
  final _window = ChatWindow();
  final _scroll = ScrollController();
  var _loading = false;
  var _resuming = false;
  String? _error;

  @override
  void initState() {
    super.initState();
    _scroll.addListener(_onScroll);
    unawaited(_page(initial: true));
  }

  @override
  void dispose() {
    _scroll.dispose();
    super.dispose();
  }

  void _onScroll() {
    if (_scroll.hasClients && nearEarliest(_scroll.position)) {
      unawaited(_page());
    }
  }

  /// 内容不满一屏时没有滚动事件，读完一页如果还贴着顶部就继续往前读。
  void _pageIfAtTop() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted && _scroll.hasClients && nearEarliest(_scroll.position)) {
        unawaited(_page());
      }
    });
  }

  /// 读一页：第一次读最后 40 条，之后从已经读到的最早一条往前读。
  Future<void> _page({bool initial = false}) async {
    final rpc = widget.model.rpc;
    if (rpc == null || _loading) return;
    if (!initial && (!_window.hasMore || _window.beforeOffset == null)) return;
    final s = widget.session;
    setState(() => _loading = true);
    try {
      final page = await rpc.call('nativeChat.readSession', {
        'agent': s.agent,
        'sessionId': s.id,
        if (s.path.isNotEmpty) 'transcriptPath': s.path,
        'limit': 40,
        if (!initial) 'beforeOffset': _window.beforeOffset,
      });
      if (!mounted) return;
      if (initial) await routeSettled(context);
      if (!mounted) return;
      final before = _window.messages.length;
      setState(() {
        _window.applyOlderPage(page);
        _error = null;
      });
      if (_window.messages.length > before) _pageIfAtTop();
    } catch (e) {
      if (mounted) setState(() => _error = friendlyError(e, context.l10n));
    } finally {
      if (mounted) setState(() => _loading = false);
    }
  }

  /// 让电脑在会话目录所在的工作区里开终端接着这个会话；已经开着的直接用那个终端。
  Future<void> _resume() async {
    final rpc = widget.model.rpc;
    if (rpc == null) return;
    setState(() => _resuming = true);
    final messenger = ScaffoldMessenger.of(context);
    final l = context.l10n;
    try {
      final res = await rpc.call('history.resume', {
        'session': widget.session.id,
      });
      final terminal = res['terminal'] as String? ?? '';
      final worktree = res['worktree'] as String? ?? '';
      messenger.showSnackBar(
        SnackBar(
          content: Text(
            res['alreadyOpen'] == true
                ? l.historyAlreadyOpen
                : l.historyResumed,
          ),
          action: SnackBarAction(
            label: l.historyViewOnPhone,
            onPressed: () => unawaited(_open(worktree, terminal)),
          ),
        ),
      );
    } catch (e) {
      if (mounted) setState(() => _error = friendlyError(e, context.l10n));
    } finally {
      if (mounted) setState(() => _resuming = false);
    }
  }

  /// 打开电脑上刚开的那个会话。新终端要等下一次刷新才出现在列表里，最多等几秒。
  Future<void> _open(String worktree, String terminal) async {
    final model = widget.model;
    for (var i = 0; i < 6; i++) {
      await model.refreshWorktrees();
      final tab = (model.tabs[worktree] ?? const <SessionTab>[])
          .where((t) => t.terminalId == terminal)
          .firstOrNull;
      if (tab != null) {
        if (mounted) openSession(context, model, tab);
        return;
      }
      await Future<void>.delayed(const Duration(milliseconds: 500));
    }
    if (mounted) setState(() => _error = context.l10n.historyNotYetOpen);
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final s = widget.session;
    final settings = widget.model.settings;
    final entries = foldTranscript(_window.messages)
        .where((e) => settings.showReasoning || e.message?.role != 'reasoning')
        .toList();
    final entryKeys = transcriptKeys(entries);
    // 和对话页一样按身份认行：读进更早的一页时，展开着的工具组不会错配到别的行上
    final rowKeys = <String>[
      for (var i = entries.length - 1; i >= 0; i--) entryKeys[i],
      if (_window.hasMore) 'earlier',
    ];
    final rowIndex = {for (var i = 0; i < rowKeys.length; i++) rowKeys[i]: i};
    return Scaffold(
      backgroundColor: Theme.of(context).scaffoldBackgroundColor,
      appBar: AppBar(
        titleSpacing: 0,
        title: Row(
          children: [
            AgentTile(agent: s.agent, size: Ic.large),
            const SizedBox(width: 10),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    s.title.isEmpty ? context.l10n.historyUntitled : s.title,
                    overflow: TextOverflow.ellipsis,
                    style: TextStyle(
                      fontSize: Fs.title,
                      fontWeight: Fw.bold,
                      color: c.text,
                    ),
                  ),
                  Text(
                    s.cwd,
                    overflow: TextOverflow.ellipsis,
                    style: TextStyle(fontSize: Fs.small, color: c.muted),
                  ),
                ],
              ),
            ),
          ],
        ),
        bottom: PreferredSize(
          preferredSize: const Size.fromHeight(1),
          child: Divider(height: 1, color: c.line),
        ),
      ),
      body: SideSafe(
        child: Column(
          children: [
            if (_error != null) NoticeBar(_error!),
            Expanded(
              child: _loading && entries.isEmpty
                  ? const Center(child: PosseLoader())
                  : SelectionArea(
                      child: Scrollbar(
                        controller: _scroll,
                        // 倒着排：一打开就在最新的消息，往上翻到头自动读更早的
                        child: ListView.builder(
                          controller: _scroll,
                          reverse: true,
                          padding: const EdgeInsets.fromLTRB(0, 8, 0, 24),
                          itemCount: rowKeys.length,
                          findChildIndexCallback: (key) =>
                              key is ValueKey<String>
                              ? rowIndex[key.value]
                              : null,
                          itemBuilder: (context, index) => KeyedSubtree(
                            key: ValueKey(rowKeys[index]),
                            child: _row(index, entries, s.agent),
                          ),
                        ),
                      ),
                    ),
            ),
            SafeArea(
              top: false,
              child: Padding(
                padding: const EdgeInsets.fromLTRB(16, 8, 16, 8),
                child: SizedBox(
                  width: double.infinity,
                  child: FilledButton.icon(
                    onPressed: _resuming || widget.model.rpc == null
                        ? null
                        : _resume,
                    icon: const Icon(Icons.laptop_mac_rounded, size: Ic.medium),
                    label: Text(
                      _resuming
                          ? context.l10n.historyOpening
                          : context.l10n.historyContinueOnComputer,
                    ),
                  ),
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _row(int index, List<TranscriptEntry> entries, String agent) {
    if (index >= entries.length) {
      return EarlierRow(loading: _loading, onLoad: () => unawaited(_page()));
    }
    final entry = entries[entries.length - 1 - index];
    if (entry.tools) {
      return ToolRun(
        blocks: entry.blocks,
        initiallyOpen: widget.model.settings.expandTools,
      );
    }
    return MessageBubble(
      message: entry.message!,
      blocks: entry.blocks,
      agent: agent,
    );
  }
}

/// 历史页的 agent 筛选：一行小标签，选中的和别处的选中项一样是淡底陶土字，没选的只是灰字。
class _AgentChip extends StatelessWidget {
  const _AgentChip({
    required this.agent,
    required this.selected,
    required this.onTap,
  });

  final String agent;
  final bool selected;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Material(
      color: selected ? c.tint : Colors.transparent,
      borderRadius: BorderRadius.circular(Rd.pill),
      child: InkWell(
        borderRadius: BorderRadius.circular(Rd.pill),
        onTap: onTap,
        child: Padding(
          padding: EdgeInsets.fromLTRB(agent.isEmpty ? 10 : 6, 6, 10, 6),
          child: Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              if (agent.isNotEmpty) ...[
                Opacity(
                  opacity: selected ? 1 : 0.6,
                  child: AgentTile(agent: agent, size: Ic.small),
                ),
                const SizedBox(width: 6),
              ],
              Text(
                agent.isEmpty ? context.l10n.commonAll : agentLabel(agent),
                style: TextStyle(
                  fontSize: Fs.small,
                  fontWeight: Fw.medium,
                  color: selected ? c.accent : c.muted,
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
