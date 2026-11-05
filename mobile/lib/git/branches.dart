import 'package:flutter/material.dart';

import '../app_state.dart';
import '../chat/transcript.dart' show agentLabel;
import '../l10n/l10n.dart';
import '../ui/line_icons.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';

/// git.branches 里的一个分支。
class GitBranch {
  const GitBranch({
    required this.name,
    required this.remote,
    required this.current,
    this.upstream = '',
    this.ahead = 0,
    this.behind = 0,
    this.time = 0,
    this.elsewhere = '',
    this.workspace = '',
  });

  final String name;
  final bool remote;
  final bool current;
  final String upstream;
  final int ahead;
  final int behind;

  /// 最后一次提交的时间（Unix 秒），按它排新旧。
  final int time;

  /// 在别的 worktree 里检出着时是那个 worktree 的目录，切不过去。
  final String elsewhere;

  /// 检出它的那个 worktree 是 Posse 的工作区时是工作区 ID，点了跳过去。
  final String workspace;

  /// 远端分支去掉远端名：origin/feat/x → feat/x。
  String get localName {
    final slash = name.indexOf('/');
    return remote && slash >= 0 ? name.substring(slash + 1) : name;
  }
}

class GitBranchList {
  const GitBranchList({
    required this.head,
    required this.operation,
    required this.branches,
  });

  final String head;

  /// 进行中的 rebase、merge 等，这时不能切换。
  final String operation;
  final List<GitBranch> branches;
}

GitBranchList parseGitBranches(Map<String, dynamic> raw) {
  int n(Object? v) => v is num ? v.toInt() : 0;
  final list = <GitBranch>[];
  for (final b in raw['branches'] as List? ?? const []) {
    if (b is! Map) continue;
    final name = b['name'];
    if (name is! String || name.isEmpty) continue;
    list.add(
      GitBranch(
        name: name,
        remote: b['remote'] == true,
        current: b['current'] == true,
        upstream: b['upstream'] as String? ?? '',
        ahead: n(b['ahead']),
        behind: n(b['behind']),
        time: n(b['time']),
        elsewhere: b['elsewhere'] as String? ?? '',
        workspace: b['workspace'] as String? ?? '',
      ),
    );
  }
  return GitBranchList(
    head: raw['head'] as String? ?? '',
    operation: raw['operation'] as String? ?? '',
    branches: list,
  );
}

/// git.checkout 的结果：切过去了，或者有 agent 在工作、要先问（[confirm] 是 agent）。
class GitCheckoutResult {
  const GitCheckoutResult({
    required this.switched,
    this.head = '',
    this.confirm = '',
  });

  final bool switched;
  final String head;
  final String confirm;
}

GitCheckoutResult parseGitCheckout(Map<String, dynamic> raw) =>
    GitCheckoutResult(
      switched: raw['switched'] == true,
      head: raw['head'] as String? ?? '',
      confirm: raw['confirm'] as String? ?? '',
    );

/// 分支列表要显示的两组：本地的当前分支在最前、其余按最近提交排；远程的去掉本地已有同名分支的
/// （切过去就是那个本地分支），同样按最近提交排。[query] 不分大小写地筛名字。
(List<GitBranch>, List<GitBranch>) branchGroups(
  List<GitBranch> all,
  String query,
) {
  final q = query.trim().toLowerCase();
  bool match(GitBranch b) => q.isEmpty || b.name.toLowerCase().contains(q);
  final localNames = {
    for (final b in all)
      if (!b.remote) b.name,
  };
  int byTime(GitBranch a, GitBranch b) => b.time.compareTo(a.time);
  final local =
      [
        for (final b in all)
          if (!b.remote && match(b)) b,
      ]..sort(
        (a, b) => a.current != b.current ? (a.current ? -1 : 1) : byTime(a, b),
      );
  final remote = [
    for (final b in all)
      if (b.remote && !localNames.contains(b.localName) && match(b)) b,
  ]..sort(byTime);
  return (local, remote);
}

/// 从底部滑上来的分支列表，点一个切过去。切换成功返回新分支名，关掉或没切返回 null。
/// [workspaceName] 把工作区 ID 换成显示的名字（找不到时为 null），[onOpenWorkspace] 跳到检出着那个分支的工作区。
Future<String?> showBranchSheet(
  BuildContext context, {
  String? repoName,
  required Future<GitBranchList> Function() load,
  required Future<GitCheckoutResult> Function(GitBranch branch, bool force)
  checkout,
  required String? Function(String id) workspaceName,
  void Function(String id)? onOpenWorkspace,
}) {
  return showModalBottomSheet<String>(
    context: context,
    isScrollControlled: true,
    showDragHandle: true,
    useSafeArea: true,
    builder: (sheet) => SizedBox(
      height: MediaQuery.sizeOf(sheet).height * 0.75,
      child: _BranchSheet(
        repoName: repoName,
        load: load,
        checkout: checkout,
        workspaceName: workspaceName,
        onOpenWorkspace: onOpenWorkspace,
      ),
    ),
  );
}

class _BranchSheet extends StatefulWidget {
  const _BranchSheet({
    required this.repoName,
    required this.load,
    required this.checkout,
    required this.workspaceName,
    required this.onOpenWorkspace,
  });

  final String? repoName;
  final Future<GitBranchList> Function() load;
  final Future<GitCheckoutResult> Function(GitBranch branch, bool force)
  checkout;
  final String? Function(String id) workspaceName;
  final void Function(String id)? onOpenWorkspace;

  @override
  State<_BranchSheet> createState() => _BranchSheetState();
}

class _BranchSheetState extends State<_BranchSheet> {
  final _query = TextEditingController();
  GitBranchList? _list;
  String? _error;

  /// 正在切过去的分支：这时别的行点不了。
  GitBranch? _switching;

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    _query.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    setState(() => _error = null);
    try {
      final list = await widget.load();
      if (mounted) setState(() => _list = list);
    } catch (error) {
      if (mounted) setState(() => _error = friendlyError(error, context.l10n));
    }
  }

  /// 切换失败后重读分支，读不出来时留着原来的列表。
  Future<GitBranchList?> _reloadQuietly() async {
    try {
      final list = await widget.load();
      if (mounted) setState(() => _list = list);
      return list;
    } catch (_) {
      return null;
    }
  }

  Future<void> _tap(GitBranch b) async {
    if (b.current || _switching != null) return;
    if (b.elsewhere.isNotEmpty) {
      final open = widget.onOpenWorkspace;
      if (b.workspace.isEmpty || open == null) return;
      Navigator.of(context).pop();
      open(b.workspace);
      return;
    }
    await _switch(b, false);
  }

  Future<void> _switch(GitBranch b, bool force) async {
    final l = context.l10n;
    setState(() {
      _switching = b;
      _error = null;
    });
    try {
      final r = await widget.checkout(b, force);
      if (!mounted) return;
      if (r.confirm.isNotEmpty) {
        setState(() => _switching = null);
        if (await _confirmAgent(r.confirm) && mounted) await _switch(b, true);
        return;
      }
      Navigator.of(context).pop(r.switched ? r.head : null);
    } catch (error) {
      if (!mounted) return;
      // 失败多半是电脑那边分支变了（别处检出、删掉），重读一遍；重读完之前行仍点不了。
      // 超时、断线时不知道电脑上切没切，重读后已经在目标分支上就当切过去了
      final list = await _reloadQuietly();
      if (!mounted) return;
      if (list != null && list.head.isNotEmpty && list.head == b.localName) {
        Navigator.of(context).pop(list.head);
        return;
      }
      setState(() {
        _switching = null;
        _error = friendlyError(error, l);
      });
    }
  }

  Future<bool> _confirmAgent(String agent) async {
    final l = context.l10n;
    return showPosseConfirm(
      context,
      title: l.gitCheckoutAgentTitle(agentLabel(agent)),
      body: l.gitCheckoutAgentBody,
      confirm: l.gitCheckoutAnyway,
      tone: ConfirmTone.warning,
    );
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final list = _list;
    final busy = list?.operation ?? '';
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 0, 16, 12),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                l.gitBranchesTitle,
                style: TextStyle(
                  fontSize: Fs.title,
                  fontWeight: Fw.bold,
                  color: c.text,
                ),
              ),
              if (widget.repoName != null) ...[
                const SizedBox(height: 2),
                Text(
                  widget.repoName!,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: TextStyle(fontSize: Fs.small, color: c.muted),
                ),
              ],
              const SizedBox(height: 12),
              SizedBox(
                height: 38,
                child: TextField(
                  controller: _query,
                  onChanged: (_) => setState(() {}),
                  textInputAction: TextInputAction.search,
                  style: TextStyle(fontSize: Fs.body, color: c.text),
                  decoration: InputDecoration(
                    isDense: true,
                    contentPadding: EdgeInsets.zero,
                    hintText: l.gitBranchesSearch,
                    prefixIcon: Icon(
                      Icons.search_rounded,
                      size: Ic.medium,
                      color: c.muted,
                    ),
                    prefixIconConstraints: const BoxConstraints.tightFor(
                      width: 38,
                      height: 38,
                    ),
                  ),
                ),
              ),
            ],
          ),
        ),
        if (_error != null) NoticeBar(_error!),
        if (busy.isNotEmpty) NoticeBar(l.gitBranchesBusy(busy)),
        Expanded(child: _body(context, c, list, busy.isEmpty)),
      ],
    );
  }

  Widget _body(
    BuildContext context,
    PosseColors c,
    GitBranchList? list,
    bool canSwitch,
  ) {
    final l = context.l10n;
    if (list == null) {
      if (_error != null) {
        return Center(
          child: TextButton(onPressed: _load, child: Text(l.commonRetry)),
        );
      }
      return const Center(child: PosseLoader());
    }
    final (local, remote) = branchGroups(list.branches, _query.text);
    if (local.isEmpty && remote.isEmpty) {
      return EmptyState(
        icon: Icons.search_off_rounded,
        text: l.gitBranchesNoMatch,
      );
    }
    return ListView(
      children: [
        if (local.isNotEmpty)
          Section(
            title: l.gitBranchesLocal,
            inset: 48,
            children: [for (final b in local) _row(c, b, canSwitch)],
          ),
        if (remote.isNotEmpty)
          Section(
            title: l.gitBranchesRemote,
            inset: 48,
            children: [for (final b in remote) _row(c, b, canSwitch)],
          ),
      ],
    );
  }

  Widget _row(PosseColors c, GitBranch b, bool canSwitch) {
    final l = context.l10n;
    String? sub;
    if (b.current) {
      sub = l.gitBranchCurrent;
    } else if (b.elsewhere.isNotEmpty) {
      final name = b.workspace.isEmpty
          ? null
          : widget.workspaceName(b.workspace);
      sub = name != null
          ? l.gitBranchInWorkspace(name)
          : l.gitBranchInWorktree(b.elsewhere.split('/').last);
    } else if (b.upstream.isNotEmpty) {
      sub = [
        l.gitTracking(b.upstream),
        if (b.ahead > 0) l.gitAhead(b.ahead),
        if (b.behind > 0) l.gitBehind(b.behind),
      ].join(' · ');
    }
    // 别处检出着、又不是 Posse 工作区的：手机上没法打开，点不了
    final jump =
        b.elsewhere.isNotEmpty &&
        b.workspace.isNotEmpty &&
        widget.onOpenWorkspace != null;
    final enabled =
        !b.current &&
        _switching == null &&
        (b.elsewhere.isEmpty ? canSwitch : jump);
    final dim = !b.current && !enabled && _switching != b;
    return RowTile(
      chevron: false,
      leading: LineIconView(
        LineIcon.branch,
        size: Ic.button,
        color: b.current ? c.accent : (dim ? c.faint : c.muted),
      ),
      title: Text(
        b.name,
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
        style: mono(
          Fs.body,
          color: b.current ? c.accent : (dim ? c.faint : c.text),
        ).copyWith(fontWeight: Fw.medium),
      ),
      subtitle: sub == null
          ? null
          : Text(
              sub,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(fontSize: Fs.small, color: c.muted),
            ),
      trailing: _switching == b
          ? const HexSpinner()
          : b.current
          ? Icon(Icons.check_rounded, size: Ic.medium, color: c.accent)
          : jump
          ? Icon(Icons.arrow_forward_rounded, size: Ic.medium, color: c.faint)
          : null,
      onTap: enabled ? () => _tap(b) : null,
    );
  }
}

/// 改动页切换分支要用的：读分支、切换（[repo] 是 git.status 给的仓库键），工作区 ID 换名字、跳过去，切完通知外面刷新。
class GitBranchActions {
  const GitBranchActions({
    required this.load,
    required this.checkout,
    required this.workspaceName,
    this.openWorkspace,
    this.onSwitched,
  });

  final Future<GitBranchList> Function(String? repo) load;
  final Future<GitCheckoutResult> Function(
    String? repo,
    GitBranch branch,
    bool force,
  )
  checkout;
  final String? Function(String id) workspaceName;
  final void Function(String id)? openWorkspace;
  final VoidCallback? onSwitched;
}
