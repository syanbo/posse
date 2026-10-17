import 'package:flutter/cupertino.dart';
import 'package:flutter/material.dart';

import '../protocol/rpc.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'status.dart';

String gitWorktree(String id) => id.startsWith('id:') ? id : 'id:$id';

Future<GitStatusView> loadGitStatus(PhoneRpc rpc, String worktree) {
  return rpc
      .call('git.status', {'worktree': gitWorktree(worktree)})
      .then(parseGitStatus);
}

Future<GitDiffView> loadGitDiff(PhoneRpc rpc, String worktree, String path) {
  return rpc
      .call('git.diff', {'worktree': gitWorktree(worktree), 'path': path})
      .then(parseGitDiff);
}

/// 单独打开的改动页。工作区页里用的是不带顶栏的 GitPane。
class GitScreen extends StatelessWidget {
  const GitScreen({
    super.key,
    required this.title,
    required this.loadStatus,
    required this.loadDiff,
  });

  final String title;
  final Future<GitStatusView> Function() loadStatus;
  final Future<GitDiffView> Function(String path) loadDiff;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: Text(title)),
      body: GitPane(loadStatus: loadStatus, loadDiff: loadDiff),
    );
  }
}

class GitPane extends StatefulWidget {
  const GitPane({super.key, required this.loadStatus, required this.loadDiff});

  final Future<GitStatusView> Function() loadStatus;
  final Future<GitDiffView> Function(String path) loadDiff;

  @override
  State<GitPane> createState() => _GitPaneState();
}

class _GitPaneState extends State<GitPane> {
  GitStatusView? _status;
  String? _error;
  var _loading = true;

  @override
  void initState() {
    super.initState();
    _reload();
  }

  Future<void> _reload() async {
    setState(() => _error = null);
    try {
      final status = await widget.loadStatus();
      if (!mounted) return;
      setState(() {
        _status = status;
        _loading = false;
      });
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _error = '$error';
        _loading = false;
      });
    }
  }

  void _open(GitFileChange file) {
    showModalBottomSheet<void>(
      context: context,
      isScrollControlled: true,
      showDragHandle: true,
      useSafeArea: true,
      builder: (context) =>
          _DiffSheet(file: file, load: () => widget.loadDiff(file.path)),
    );
  }

  @override
  Widget build(BuildContext context) {
    final status = _status;
    if (_loading) return const Center(child: CircularProgressIndicator());
    if (_error != null && status == null) {
      return EmptyState(
        icon: Icons.cloud_off_rounded,
        text: _error!,
        error: true,
        action: TextButton(onPressed: _reload, child: const Text('重试')),
      );
    }
    final files = status?.files ?? const <GitFileChange>[];
    return CustomScrollView(
      physics: const BouncingScrollPhysics(
        parent: AlwaysScrollableScrollPhysics(),
      ),
      slivers: [
        CupertinoSliverRefreshControl(onRefresh: _reload),
        SliverList.list(
          children: [
            if (_error != null) NoticeBar(_error!),
            if (status != null) _BranchCard(status: status),
            if (files.isEmpty)
              const Padding(
                padding: EdgeInsets.only(top: 48),
                child: EmptyState(
                  icon: Icons.check_circle_outline_rounded,
                  text: '工作区是干净的，没有改动',
                ),
              )
            else
              Section(
                title: '${files.length} 个文件有改动',
                inset: 40,
                children: [for (final file in files) _fileRow(context, file)],
              ),
            const SizedBox(height: 32),
          ],
        ),
      ],
    );
  }

  Widget _fileRow(BuildContext context, GitFileChange file) {
    final c = HiveColors.of(context);
    final slash = file.path.lastIndexOf('/');
    final name = slash < 0 ? file.path : file.path.substring(slash + 1);
    final dir = slash < 0 ? '' : file.path.substring(0, slash);
    return RowTile(
      leading: SizedBox(
        width: 14,
        child: Text(
          _letter(file),
          style: mono(
            13,
            color: _letterColor(c, file),
          ).copyWith(fontWeight: FontWeight.w600),
        ),
      ),
      title: Text(name),
      subtitle: dir.isEmpty
          ? null
          : Text(dir, style: mono(12, color: c.muted), maxLines: 1),
      trailing: _Stats(file: file),
      onTap: () => _open(file),
    );
  }
}

/// 手机拿到的改动里没有 git 的状态字母，只分得出冲突、未跟踪和其余的改动。
String _letter(GitFileChange file) {
  if (file.conflict) return 'U';
  if (file.untracked) return '?';
  return 'M';
}

Color _letterColor(HiveColors c, GitFileChange file) {
  if (file.conflict) return c.danger;
  if (file.untracked) return c.sage;
  return c.accent;
}

class _Stats extends StatelessWidget {
  const _Stats({required this.file});

  final GitFileChange file;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    const tnum = [FontFeature.tabularFigures()];
    if (file.binary || (file.added == 0 && file.deleted == 0)) {
      final label = file.stats;
      if (label.isEmpty) return const SizedBox.shrink();
      return Text(label, style: TextStyle(fontSize: 12, color: c.faint));
    }
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        if (file.added > 0)
          Text(
            '+${file.added}',
            style: TextStyle(fontSize: 13, color: c.sage, fontFeatures: tnum),
          ),
        if (file.added > 0 && file.deleted > 0) const SizedBox(width: 6),
        if (file.deleted > 0)
          Text(
            '−${file.deleted}',
            style: TextStyle(fontSize: 13, color: c.danger, fontFeatures: tnum),
          ),
      ],
    );
  }
}

class _BranchCard extends StatelessWidget {
  const _BranchCard({required this.status});

  final GitStatusView status;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final chips = <(IconData, String, Color)>[
      if (status.ahead > 0)
        (Icons.arrow_upward_rounded, '领先 ${status.ahead}', c.muted),
      if (status.behind > 0)
        (Icons.arrow_downward_rounded, '落后 ${status.behind}', c.muted),
      if (status.operation.isNotEmpty)
        (Icons.warning_amber_rounded, status.operation, c.honey),
    ];
    return Section(
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(14, 12, 14, 12),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Icon(Icons.call_split_rounded, size: 18, color: c.accent),
                  const SizedBox(width: 8),
                  Expanded(
                    child: Text(
                      status.branch.isEmpty ? '分离头指针' : status.branch,
                      overflow: TextOverflow.ellipsis,
                      style: mono(
                        14,
                        color: c.text,
                      ).copyWith(fontWeight: FontWeight.w500),
                    ),
                  ),
                ],
              ),
              if (status.upstream.isNotEmpty || chips.isNotEmpty) ...[
                const SizedBox(height: 6),
                Wrap(
                  spacing: 12,
                  runSpacing: 4,
                  children: [
                    if (status.upstream.isNotEmpty)
                      Text(
                        '跟踪 ${status.upstream}',
                        style: TextStyle(fontSize: 12, color: c.muted),
                      ),
                    for (final (icon, text, color) in chips)
                      Row(
                        mainAxisSize: MainAxisSize.min,
                        children: [
                          Icon(icon, size: 14, color: color),
                          const SizedBox(width: 3),
                          Text(
                            text,
                            style: TextStyle(fontSize: 12, color: color),
                          ),
                        ],
                      ),
                  ],
                ),
              ],
            ],
          ),
        ),
      ],
    );
  }
}

class _DiffSheet extends StatefulWidget {
  const _DiffSheet({required this.file, required this.load});

  final GitFileChange file;
  final Future<GitDiffView> Function() load;

  @override
  State<_DiffSheet> createState() => _DiffSheetState();
}

class _DiffSheetState extends State<_DiffSheet> {
  GitDiffView? _diff;
  String? _error;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final diff = await widget.load();
      if (mounted) setState(() => _diff = diff);
    } catch (error) {
      if (mounted) setState(() => _error = '$error');
    }
  }

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final height = MediaQuery.sizeOf(context).height * 0.82;
    final diff = _diff;
    return SizedBox(
      height: height,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(20, 0, 20, 10),
            child: Row(
              children: [
                Expanded(
                  child: Text(
                    widget.file.path,
                    style: mono(
                      14,
                      color: c.text,
                    ).copyWith(fontWeight: FontWeight.w500),
                  ),
                ),
                _Stats(file: widget.file),
              ],
            ),
          ),
          if (diff?.truncated == true)
            Padding(
              padding: const EdgeInsets.fromLTRB(20, 0, 20, 8),
              child: Text(
                '差异太长，只显示前面一部分',
                style: TextStyle(fontSize: 12, color: c.honey),
              ),
            ),
          Divider(height: 1, color: c.line),
          Expanded(
            child: _error != null
                ? EmptyState(
                    icon: Icons.error_outline_rounded,
                    text: _error!,
                    error: true,
                  )
                : diff == null
                ? const Center(child: CircularProgressIndicator())
                : diff.diff.isEmpty
                ? const EmptyState(icon: Icons.notes_rounded, text: '没有差异')
                : _DiffLines(text: diff.diff),
          ),
        ],
      ),
    );
  }
}

/// unified diff 按行上色：新增绿底、删除红底、段头淡底。长行横向滚动，不折行，对得齐。
class _DiffLines extends StatelessWidget {
  const _DiffLines({required this.text});

  final String text;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final lines = text.split('\n');
    if (lines.isNotEmpty && lines.last.isEmpty) lines.removeLast();
    // 跳过 diff --git、index、---、+++ 这些文件头，标题里已经有路径
    final start = lines.indexWhere((l) => l.startsWith('@@'));
    final body = start < 0 ? lines : lines.sublist(start);
    final alpha = c.dark ? 0.16 : 0.10;
    return Scrollbar(
      child: SingleChildScrollView(
        padding: const EdgeInsets.only(bottom: 24),
        child: SingleChildScrollView(
          scrollDirection: Axis.horizontal,
          child: SelectionArea(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                for (final line in body)
                  Container(
                    color: line.startsWith('@@')
                        ? c.fill
                        : line.startsWith('+')
                        ? c.sage.withValues(alpha: alpha)
                        : line.startsWith('-')
                        ? c.danger.withValues(alpha: alpha)
                        : null,
                    constraints: BoxConstraints(
                      minWidth: MediaQuery.sizeOf(context).width,
                    ),
                    padding: const EdgeInsets.symmetric(
                      horizontal: 16,
                      vertical: 1,
                    ),
                    child: Text(
                      line.isEmpty ? ' ' : line,
                      softWrap: false,
                      style: mono(
                        12,
                        height: 1.55,
                        color: line.startsWith('@@') ? c.muted : c.text,
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
