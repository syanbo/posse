import 'dart:async';

import 'package:flutter/cupertino.dart';
import 'package:flutter/material.dart';

import '../app_state.dart';
import '../chat/chat.dart';
import '../chat/merge.dart';
import '../chat/transcript.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../workspace/models.dart';
import '../workspace/workspace.dart';
import 'models.dart';

/// 电脑上的会话历史：顶上搜索，下面按时间分组。照桌面的历史页，只读；点开看对话，
/// 底部"在电脑上继续"让电脑在这个会话的目录里开终端接着聊。
class HistoryScreen extends StatefulWidget {
  const HistoryScreen({super.key, required this.model});

  final HiveAppModel model;

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
        _error = '还没连上电脑';
      });
      return;
    }
    setState(() => _loading = true);
    final q = _query.text.trim();
    try {
      final result = q.isEmpty
          ? await rpc.call('history.list', {'limit': 300})
          : await rpc.call('history.search', {'query': q, 'limit': 100});
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
            ? '电脑上的 Hive 太旧，更新后才能看历史'
            : friendlyError(e);
      });
    }
  }

  void _onQuery(String _) {
    _debounce?.cancel();
    _debounce = Timer(const Duration(milliseconds: 300), _load);
  }

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final now = DateTime.now();
    final searching = _query.text.trim().isNotEmpty;
    final groups = groupHistory(_sessions, now);
    return Scaffold(
      appBar: AppBar(title: const Text('历史')),
      body: Column(
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 4, 16, 8),
            child: TextField(
              controller: _query,
              onChanged: _onQuery,
              textInputAction: TextInputAction.search,
              onSubmitted: (_) => _load(),
              decoration: InputDecoration(
                hintText: '搜索提问、回复和工具输出',
                prefixIcon: Icon(Icons.search_rounded, color: c.muted),
                suffixIcon: searching
                    ? IconButton(
                        tooltip: '清除',
                        icon: Icon(Icons.close_rounded, color: c.muted),
                        onPressed: () {
                          _query.clear();
                          unawaited(_load());
                        },
                      )
                    : null,
              ),
            ),
          ),
          if (_loading) const LinearProgressIndicator(minHeight: 2),
          if (_error != null) NoticeBar(_error!),
          if (searching && _any && _sessions.isNotEmpty)
            const NoticeBar('没有同时含所有词的会话，下面是含其中任一个词的'),
          Expanded(
            child: _sessions.isEmpty && !_loading && _error == null
                ? EmptyState(
                    icon: Icons.history_rounded,
                    text: searching ? '没有找到' : '电脑上还没有会话历史',
                  )
                : CustomScrollView(
                    physics: const BouncingScrollPhysics(
                      parent: AlwaysScrollableScrollPhysics(),
                    ),
                    slivers: [
                      CupertinoSliverRefreshControl(onRefresh: _load),
                      // 搜索结果按相关度排，不分组
                      if (searching)
                        SliverToBoxAdapter(
                          child: Section(
                            inset: 52,
                            children: [
                              for (final s in _sessions) _row(context, s, now),
                            ],
                          ),
                        )
                      else
                        for (final (label, list) in groups)
                          SliverToBoxAdapter(
                            child: Section(
                              title: label,
                              inset: 52,
                              children: [
                                for (final s in list) _row(context, s, now),
                              ],
                            ),
                          ),
                      const SliverToBoxAdapter(child: SizedBox(height: 24)),
                    ],
                  ),
          ),
        ],
      ),
    );
  }

  Widget _row(BuildContext context, HistorySession s, DateTime now) {
    final c = HiveColors.of(context);
    final where = [
      if (s.folder.isNotEmpty) s.folder,
      if (s.branch.isNotEmpty) s.branch,
    ].join('  ');
    return RowTile(
      leading: AgentTile(agent: s.agent, size: 28),
      title: Text(
        s.title.isEmpty ? '（没有标题）' : s.title,
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
                style: TextStyle(fontSize: Fs.meta, color: c.text, height: 1.4),
              ),
            ),
          Row(
            children: [
              Expanded(
                child: Text(
                  where,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: TextStyle(fontSize: Fs.caption, color: c.muted),
                ),
              ),
              const SizedBox(width: 8),
              Text(
                historyTime(s.updatedAt, now),
                style: TextStyle(
                  fontSize: Fs.caption,
                  color: c.faint,
                  fontFeatures: const [FontFeature.tabularFigures()],
                ),
              ),
            ],
          ),
        ],
      ),
      onTap: () => Navigator.of(context).push(
        MaterialPageRoute<void>(
          builder: (_) => HistorySessionScreen(model: widget.model, session: s),
        ),
      ),
    );
  }
}

/// 一个历史会话的对话，只读。往上翻读更早的，底部"在电脑上继续"。
class HistorySessionScreen extends StatefulWidget {
  const HistorySessionScreen({
    super.key,
    required this.model,
    required this.session,
  });

  final HiveAppModel model;
  final HistorySession session;

  @override
  State<HistorySessionScreen> createState() => _HistorySessionScreenState();
}

class _HistorySessionScreenState extends State<HistorySessionScreen> {
  final _window = ChatWindow();
  var _loading = false;
  var _resuming = false;
  String? _error;

  @override
  void initState() {
    super.initState();
    unawaited(_page(initial: true));
  }

  /// 读一页：第一次读最后 40 条，之后从已经读到的最早一条往前读。
  Future<void> _page({bool initial = false}) async {
    final rpc = widget.model.rpc;
    if (rpc == null || _loading) return;
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
      setState(() {
        _window.applyOlderPage(page);
        _error = null;
      });
    } catch (e) {
      if (mounted) setState(() => _error = friendlyError(e));
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
    try {
      final res = await rpc.call('history.resume', {
        'session': widget.session.id,
      });
      final terminal = res['terminal'] as String? ?? '';
      final worktree = res['worktree'] as String? ?? '';
      messenger.showSnackBar(
        SnackBar(
          content: Text(
            res['alreadyOpen'] == true ? '这个会话已经在电脑上开着' : '已在电脑上接着这个会话',
          ),
          action: SnackBarAction(
            label: '在手机上看',
            onPressed: () => unawaited(_open(worktree, terminal)),
          ),
        ),
      );
    } catch (e) {
      if (mounted) setState(() => _error = friendlyError(e));
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
    if (mounted) setState(() => _error = '电脑上的会话还没出现，稍后在首页找它');
  }

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final s = widget.session;
    final settings = widget.model.settings;
    final entries = foldTranscript(_window.messages)
        .where((e) => settings.showReasoning || e.message?.role != 'reasoning')
        .toList();
    return Scaffold(
      appBar: AppBar(
        titleSpacing: 0,
        title: Row(
          children: [
            AgentTile(agent: s.agent, size: 28),
            const SizedBox(width: 10),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    s.title.isEmpty ? '（没有标题）' : s.title,
                    overflow: TextOverflow.ellipsis,
                    style: TextStyle(
                      fontSize: 16,
                      fontWeight: FontWeight.w600,
                      color: c.text,
                    ),
                  ),
                  Text(
                    s.cwd,
                    overflow: TextOverflow.ellipsis,
                    style: TextStyle(fontSize: 12, color: c.muted),
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
      body: Column(
        children: [
          if (_loading && entries.isEmpty)
            const LinearProgressIndicator(minHeight: 2),
          if (_error != null) NoticeBar(_error!),
          Expanded(
            child: SelectionArea(
              // 倒着排：一打开就在最新的消息，往上翻到头读更早的
              child: ListView.builder(
                reverse: true,
                padding: const EdgeInsets.fromLTRB(0, 8, 0, 8),
                itemCount: entries.length + (_window.hasMore ? 1 : 0),
                itemBuilder: (context, index) {
                  if (index == entries.length) return _earlier(c);
                  final entry = entries[entries.length - 1 - index];
                  if (entry.tools) {
                    return ToolRun(
                      blocks: entry.blocks,
                      initiallyOpen: settings.expandTools,
                    );
                  }
                  return MessageBubble(
                    message: entry.message!,
                    blocks: entry.blocks,
                    agent: s.agent,
                  );
                },
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
                  icon: const Icon(Icons.laptop_mac_rounded, size: 20),
                  label: Text(_resuming ? '正在打开…' : '在电脑上继续'),
                ),
              ),
            ),
          ),
        ],
      ),
    );
  }

  Widget _earlier(HiveColors c) {
    return SizedBox(
      height: 44,
      child: Center(
        child: _loading
            ? const SizedBox.square(
                dimension: 18,
                child: CircularProgressIndicator(strokeWidth: 2),
              )
            : TextButton(
                style: TextButton.styleFrom(
                  backgroundColor: Colors.transparent,
                  foregroundColor: c.muted,
                ),
                onPressed: _page,
                child: const Text('更早的消息'),
              ),
      ),
    );
  }
}
