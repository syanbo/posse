import 'dart:async';

import 'package:flutter/material.dart';

import '../app_state.dart';
import '../chat/transcript.dart' show chatWho;
import '../l10n/l10n.dart';
import '../ui/line_icons.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'models.dart';
import 'workspace.dart';

/// 打开新建会话的面板。[worktree] 为空时先在面板里选工作区。
Future<void> showNewSession(
  BuildContext context,
  PosseAppModel model, {
  WorktreeRow? worktree,
}) {
  return showModalBottomSheet<void>(
    context: context,
    isScrollControlled: true,
    showDragHandle: true,
    useSafeArea: true,
    builder: (_) =>
        _NewSessionSheet(model: model, worktree: worktree, host: context),
  );
}

class _NewSessionSheet extends StatefulWidget {
  const _NewSessionSheet({
    required this.model,
    required this.worktree,
    required this.host,
  });

  final PosseAppModel model;
  final WorktreeRow? worktree;

  /// 面板外面的页面：建好以后从这里打开会话。
  final BuildContext host;

  @override
  State<_NewSessionSheet> createState() => _NewSessionSheetState();
}

class _NewSessionSheetState extends State<_NewSessionSheet> {
  final _prompt = TextEditingController();
  late WorktreeRow? _worktree = widget.worktree ?? _defaultWorktree();

  /// 选中的 agent，null 是普通终端；没点过时跟着电脑给的列表选第一个。
  late String? _agent = widget.model.agents.firstOrNull;
  var _picked = false;

  /// 上次画的 agent 列表：模型每 3 秒通知一次，列表换了才重画。
  late List<String> _shown = widget.model.agents;
  var _busy = false;
  String? _error;

  /// 没指定工作区时默认最近有动静的那个。
  WorktreeRow? _defaultWorktree() {
    final rows = List.of(widget.model.worktrees)
      ..sort((a, b) => b.lastActivityAt.compareTo(a.lastActivityAt));
    return rows.firstOrNull;
  }

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_agentsChanged);
    unawaited(widget.model.refreshAgents());
  }

  /// 电脑给的 agent 列表回来或变了：重画选项，没点过的跟着选第一个，选中的被关掉了也换成第一个。
  /// 正在启动时不换：等新会话出现还要用选中的 agent。
  void _agentsChanged() {
    final agents = widget.model.agents;
    if (identical(agents, _shown)) return;
    setState(() {
      _shown = agents;
      final keep = _picked && (_agent == null || agents.contains(_agent));
      if (!keep && !_busy) _agent = agents.firstOrNull;
    });
  }

  @override
  void dispose() {
    widget.model.removeListener(_agentsChanged);
    _prompt.dispose();
    super.dispose();
  }

  Future<void> _pickWorktree() async {
    final picked = await showModalBottomSheet<WorktreeRow>(
      context: context,
      isScrollControlled: true,
      showDragHandle: true,
      useSafeArea: true,
      builder: (sheet) {
        final c = PosseColors.of(sheet);
        return SizedBox(
          height: MediaQuery.sizeOf(sheet).height * 0.7,
          child: ListView(
            children: [
              Section(
                title: sheet.l10n.sessionPickWorkspace,
                inset: 48,
                children: [
                  for (final row in widget.model.worktrees)
                    RowTile(
                      chevron: false,
                      leading: LineIconView(
                        LineIcon.folder,
                        size: Ic.button,
                        color: c.accent,
                      ),
                      title: Text(worktreeLabel(row)),
                      subtitle: Text(
                        row.branch.isNotEmpty
                            ? row.branch
                            : tildePath(row.path),
                        style: mono(Fs.small, color: c.muted),
                      ),
                      trailing: row.id == _worktree?.id
                          ? Icon(Icons.check_rounded, color: c.accent)
                          : null,
                      onTap: () => Navigator.of(sheet).pop(row),
                    ),
                ],
              ),
            ],
          ),
        );
      },
    );
    if (picked != null) setState(() => _worktree = picked);
  }

  Future<void> _start() async {
    final rpc = widget.model.rpc;
    final row = _worktree;
    if (rpc == null || row == null) return;
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final prompt = _prompt.text.trim();
      final res = await rpc.call('session.tabs.createTerminal', {
        'worktree': row.id,
        'agent': ?_agent,
        if (_agent != null && prompt.isNotEmpty) 'agentPrompt': prompt,
      });
      final tab = res['tab'];
      final terminal = tab is Map ? tab['terminal'] as String? : null;
      if (terminal == null) throw const PhoneError(PhoneError.noNewTerminal);
      final opened = await _waitForTab(row.id, terminal);
      if (!mounted) return;
      Navigator.of(context).pop();
      if (widget.host.mounted) openSession(widget.host, widget.model, opened);
    } catch (error) {
      if (mounted) {
        setState(() {
          _busy = false;
          _error = friendlyError(error, context.l10n);
        });
      }
    }
  }

  /// agent 启动后要过一会儿才通过 hooks 报上会话编号，有了编号才能开对话页。
  /// 最多等 4 秒，等不到就先按终端打开，之后从终端页右上角也能切到对话。
  Future<SessionTab> _waitForTab(String worktree, String terminal) async {
    final fallback = SessionTab(
      worktreeId: worktree,
      title: _agent ?? 'shell',
      terminalId: terminal,
      agent: _agent,
    );
    if (_agent == null) return fallback;
    final deadline = DateTime.now().add(const Duration(seconds: 4));
    var latest = fallback;
    while (DateTime.now().isBefore(deadline)) {
      final rpc = widget.model.rpc;
      if (rpc == null) break;
      try {
        final res = await rpc.call('session.tabs.list', {'worktree': worktree});
        for (final tab in parseTabs(worktree, res['tabs'])) {
          if (tab.terminalId != terminal) continue;
          latest = tab;
          if (tab.eligible) return tab;
        }
      } catch (_) {}
      await Future<void>.delayed(const Duration(milliseconds: 500));
    }
    unawaited(widget.model.refreshWorktrees());
    return latest;
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final row = _worktree;
    return Padding(
      padding: EdgeInsets.only(bottom: MediaQuery.viewInsetsOf(context).bottom),
      child: SingleChildScrollView(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(20, 0, 20, 14),
              child: Text(
                l.sessionNew,
                style: TextStyle(
                  fontSize: Fs.title,
                  fontWeight: Fw.bold,
                  color: c.text,
                ),
              ),
            ),
            Section(
              title: l.sessionWorkspace,
              inset: 48,
              children: [
                RowTile(
                  leading: LineIconView(
                    LineIcon.folder,
                    size: Ic.button,
                    color: c.accent,
                  ),
                  title: Text(
                    row == null ? l.sessionNoWorkspaces : worktreeLabel(row),
                  ),
                  subtitle: row == null
                      ? null
                      : Text(
                          row.branch.isNotEmpty
                              ? row.branch
                              : tildePath(row.path),
                          style: mono(Fs.small, color: c.muted),
                        ),
                  // 从工作区详情页进来时工作区已经定了
                  onTap: widget.worktree == null ? _pickWorktree : null,
                ),
              ],
            ),
            Padding(
              padding: const EdgeInsets.fromLTRB(20, 0, 20, 8),
              child: Text(
                l.sessionOpenWith,
                style: TextStyle(
                  fontSize: Fs.small,
                  fontWeight: Fw.medium,
                  color: c.muted,
                ),
              ),
            ),
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 0, 16, 16),
              child: Row(
                children: [
                  for (final agent in <String?>[..._shown, null])
                    Expanded(child: _agentChoice(c, agent)),
                ],
              ),
            ),
            if (_agent != null)
              Padding(
                padding: const EdgeInsets.fromLTRB(16, 0, 16, 12),
                child: TextField(
                  controller: _prompt,
                  minLines: 2,
                  maxLines: 6,
                  decoration: InputDecoration(
                    hintText: l.sessionPromptHint,
                    fillColor: c.card,
                  ),
                ),
              ),
            if (_error != null) NoticeBar(_error!),
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 4, 16, 16),
              child: FilledButton(
                onPressed: _busy || row == null || widget.model.rpc == null
                    ? null
                    : _start,
                child: Text(
                  _busy
                      ? l.sessionStarting
                      : _agent == null
                      ? l.sessionOpenTerminal
                      : l.sessionStartChat(chatWho(l, 'assistant', _agent)),
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _agentChoice(PosseColors c, String? agent) {
    final selected = agent == _agent;
    return GestureDetector(
      behavior: HitTestBehavior.opaque,
      onTap: () => setState(() {
        _agent = agent;
        _picked = true;
      }),
      child: AnimatedContainer(
        duration: const Duration(milliseconds: 150),
        margin: const EdgeInsets.symmetric(horizontal: 4),
        padding: const EdgeInsets.symmetric(vertical: 10),
        decoration: BoxDecoration(
          color: selected ? c.tint : c.card,
          borderRadius: BorderRadius.circular(Rd.input),
          border: Border.all(
            color: selected ? c.accent : Colors.transparent,
            width: 1.5,
          ),
        ),
        child: Column(
          children: [
            AgentTile(agent: agent, size: Ic.large),
            const SizedBox(height: 6),
            Text(
              agent == null
                  ? context.l10n.sessionTerminal
                  : chatWho(context.l10n, 'assistant', agent),
              style: TextStyle(
                fontSize: Fs.small,
                fontWeight: Fw.medium,
                color: selected ? c.accent : c.text,
              ),
            ),
          ],
        ),
      ),
    );
  }
}
