import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../protocol/rpc.dart';
import '../ui/code.dart';
import '../ui/file_icons.dart';
import '../ui/highlight.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'status.dart';

String gitWorktree(String id) => id.startsWith('id:') ? id : 'id:$id';

Future<GitStatusView> loadGitStatus(PhoneRpc rpc, String worktree) {
  return rpc
      .call('git.status', {'worktree': gitWorktree(worktree)})
      .then(parseGitStatus);
}

Future<GitDiffView> loadGitDiff(
  PhoneRpc rpc,
  String worktree,
  GitFileChange file,
) {
  return rpc
      .call('git.diff', {
        'worktree': gitWorktree(worktree),
        'path': file.path,
        if (file.orig.isNotEmpty) 'orig': file.orig,
      })
      .then(parseGitDiff);
}

/// 单独打开的改动页。工作区页里用的是不带顶栏的 GitPane。
class GitScreen extends StatelessWidget {
  const GitScreen({
    super.key,
    required this.title,
    required this.loadStatus,
    required this.loadDiff,
    this.onOpenFile,
  });

  final String title;
  final Future<GitStatusView> Function() loadStatus;
  final Future<GitDiffView> Function(GitFileChange file) loadDiff;
  final void Function(String path)? onOpenFile;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: Text(title)),
      body: GitPane(
        loadStatus: loadStatus,
        loadDiff: loadDiff,
        onOpenFile: onOpenFile,
      ),
    );
  }
}

/// 改动多久自己刷新一次，和首页的工作区列表一样。
const gitPoll = Duration(seconds: 3);

// 树形视图：每层缩进，和行首展开箭头的宽度（文件行空出它，和目录的文件夹图标对齐）
const _treeIndent = 16.0;
const _arrowBox = Ic.medium;

class GitPane extends StatefulWidget {
  const GitPane({
    super.key,
    required this.loadStatus,
    required this.loadDiff,
    this.onOpenFile,
    this.active = true,
  });

  /// 这一页看得见：和文件页放在同一个 IndexedStack 里，切走时不刷新。
  final bool active;

  final Future<GitStatusView> Function() loadStatus;
  final Future<GitDiffView> Function(GitFileChange file) loadDiff;

  /// 从 diff 页跳到这个文件的完整内容；为空时 diff 页不显示这个按钮。
  final void Function(String path)? onOpenFile;

  @override
  State<GitPane> createState() => _GitPaneState();
}

class _GitPaneState extends State<GitPane> {
  /// 按目录还是平铺，这次打开 App 期间各工作区共用。
  static var _tree = true;

  /// 收起的组和目录：组是 `ChangeKind`，目录是 `ChangeKind:路径`。
  final _folded = <String>{};
  GitStatusView? _status;
  String? _error;
  var _loading = true;
  Timer? _poll;
  var _busy = false;

  @override
  void initState() {
    super.initState();
    _reload();
    // 不下拉刷新：看得见时定时读，agent 改了文件过一会儿就出来
    _poll = Timer.periodic(gitPoll, (_) => _tick());
  }

  @override
  void didUpdateWidget(GitPane old) {
    super.didUpdateWidget(old);
    if (widget.active && !old.active) _tick();
  }

  @override
  void dispose() {
    _poll?.cancel();
    super.dispose();
  }

  /// 定时刷新：上面盖着 diff 页、切到文件页、上一次还没回来时跳过；出错留着上次的列表，只挂提示。
  Future<void> _tick() async {
    if (_busy || _loading || !mounted || !widget.active) return;
    if (!(ModalRoute.of(context)?.isCurrent ?? true)) return;
    _busy = true;
    try {
      final status = await widget.loadStatus();
      if (!mounted) return;
      setState(() {
        _status = status;
        _error = null;
      });
    } catch (error) {
      if (mounted) setState(() => _error = friendlyError(error, context.l10n));
    } finally {
      _busy = false;
    }
  }

  Future<void> _reload() async {
    setState(() => _error = null);
    try {
      final status = await widget.loadStatus();
      if (!mounted) return;
      await routeSettled(context);
      if (!mounted) return;
      setState(() {
        _status = status;
        _loading = false;
      });
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _error = friendlyError(error, context.l10n);
        _loading = false;
      });
    }
  }

  void _open(GitFileChange file) {
    final open = widget.onOpenFile;
    Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => DiffViewer(
          file: file,
          load: () => widget.loadDiff(file),
          onOpenFile: open == null ? null : () => open(file.path),
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final status = _status;
    if (_loading) return const Center(child: PosseLoader());
    if (_error != null && status == null) {
      return EmptyState(
        icon: Icons.cloud_off_rounded,
        text: _error!,
        error: true,
        action: TextButton(
          onPressed: _reload,
          child: Text(context.l10n.commonRetry),
        ),
      );
    }
    final c = PosseColors.of(context);
    final files = status?.files ?? const <GitFileChange>[];
    return CustomScrollView(
      slivers: [
        SliverList.list(
          children: [
            if (_error != null) NoticeBar(_error!),
            if (status != null) _BranchCard(status: status),
            if (files.isEmpty)
              Padding(
                padding: const EdgeInsets.only(top: 32),
                child: EmptyState(
                  icon: Icons.check_circle_outline_rounded,
                  text: context.l10n.gitClean,
                ),
              )
            else
              _listHeader(context, c, files.length),
          ],
        ),
        if (files.isNotEmpty)
          for (final kind in _groups)
            if (files.any((f) => _groupOf(f) == kind))
              _group(context, kind, switch (kind) {
                ChangeKind.conflict => context.l10n.gitConflicts,
                ChangeKind.untracked => context.l10n.gitUnversioned,
                _ => context.l10n.gitChanges,
              }, files.where((f) => _groupOf(f) == kind).toList()),
        const SliverToBoxAdapter(child: SizedBox(height: 32)),
      ],
    );
  }

  /// 照桌面端分组：冲突、改动、未跟踪。
  static const _groups = [
    ChangeKind.conflict,
    ChangeKind.modified,
    ChangeKind.untracked,
  ];

  static ChangeKind _groupOf(GitFileChange f) => switch (f.kind) {
    ChangeKind.conflict => ChangeKind.conflict,
    ChangeKind.untracked => ChangeKind.untracked,
    _ => ChangeKind.modified,
  };

  Widget _listHeader(BuildContext context, PosseColors c, int count) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(20, 0, 16, 8),
      child: Row(
        children: [
          Expanded(
            child: Text(
              context.l10n.gitChangedFiles(count),
              style: TextStyle(fontSize: Fs.small, color: c.muted),
            ),
          ),
          _ModeSwitch(tree: _tree, onChanged: (v) => setState(() => _tree = v)),
        ],
      ),
    );
  }

  Widget _group(
    BuildContext context,
    ChangeKind kind,
    String label,
    List<GitFileChange> files,
  ) {
    final c = PosseColors.of(context);
    final folded = _folded.contains('$kind');
    final rows = _tree
        ? changeTree(
            files,
            folded: {
              for (final k in _folded)
                if (k.startsWith('$kind:')) k.substring('$kind:'.length),
            },
          )
        : [
            for (final f in [
              ...files,
            ]..sort((a, b) => a.path.compareTo(b.path)))
              ChangeRow(key: f.path, label: f.name, depth: 0, file: f),
          ];
    final header = _foldRow(
      c,
      open: !folded,
      onTap: () => _fold('$kind'),
      depth: 0,
      child: Row(
        children: [
          Text(
            label,
            style: TextStyle(
              fontSize: Fs.small,
              fontWeight: Fw.medium,
              color: kind == ChangeKind.conflict ? c.danger : c.text,
            ),
          ),
          const SizedBox(width: 8),
          Text(
            '${files.length}',
            style: TextStyle(fontSize: Fs.small, color: c.faint),
          ),
        ],
      ),
    );
    const radius = Radius.circular(Rd.card);
    // 改动多时一次全建，数据到的那一帧要几十毫秒；按需建行，装饰不裁剪，标题行自己裁圆角
    return SliverPadding(
      padding: const EdgeInsets.fromLTRB(16, 0, 16, 12),
      sliver: DecoratedSliver(
        decoration: BoxDecoration(
          color: c.card,
          borderRadius: BorderRadius.circular(Rd.card),
        ),
        sliver: SliverList.builder(
          itemCount: folded ? 1 : rows.length + 2,
          itemBuilder: (context, i) {
            if (i == 0) {
              return ClipRRect(
                borderRadius: BorderRadius.vertical(
                  top: radius,
                  bottom: folded ? radius : Radius.zero,
                ),
                child: header,
              );
            }
            if (i > rows.length) return const SizedBox(height: 4);
            final row = rows[i - 1];
            return row.file == null
                ? _dirRow(c, kind, row)
                : _fileRow(c, row.file!, row.depth + 1, flat: !_tree);
          },
        ),
      ),
    );
  }

  void _fold(String key) => setState(() {
    if (!_folded.remove(key)) _folded.add(key);
  });

  /// 带展开箭头的一行：组标题和目录共用。
  Widget _foldRow(
    PosseColors c, {
    required bool open,
    required VoidCallback onTap,
    required int depth,
    required Widget child,
  }) {
    return InkWell(
      onTap: onTap,
      child: SizedBox(
        height: 40,
        child: Padding(
          padding: EdgeInsets.only(left: 8 + depth * _treeIndent, right: 14),
          child: Row(
            children: [
              AnimatedRotation(
                turns: open ? 0.25 : 0,
                duration: const Duration(milliseconds: 150),
                child: Icon(
                  Icons.chevron_right_rounded,
                  size: Ic.medium,
                  color: c.faint,
                ),
              ),
              const SizedBox(width: 4),
              Expanded(child: child),
            ],
          ),
        ),
      ),
    );
  }

  Widget _dirRow(PosseColors c, ChangeKind kind, ChangeRow row) {
    final key = '$kind:${row.key}';
    final open = !_folded.contains(key);
    return _foldRow(
      c,
      open: open,
      onTap: () => _fold(key),
      depth: row.depth + 1,
      child: Row(
        children: [
          FileIconView(row.label, size: Ic.medium, dir: true, open: open),
          const SizedBox(width: 8),
          Flexible(
            child: Text(
              row.label,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: mono(Fs.small, color: c.text),
            ),
          ),
          const SizedBox(width: 8),
          Text(
            '${row.count}',
            style: TextStyle(fontSize: Fs.small, color: c.faint),
          ),
        ],
      ),
    );
  }

  Widget _fileRow(
    PosseColors c,
    GitFileChange file,
    int depth, {
    required bool flat,
  }) {
    final kind = file.kind;
    final deleted = kind == ChangeKind.deleted;
    final from = kind == ChangeKind.renamed && file.orig.isNotEmpty
        ? file.orig.substring(file.orig.lastIndexOf('/') + 1)
        : null;
    // 平铺时目录和改名的原名都要写；从别的目录挪过来的写原来的全路径，同目录改名只写原名
    final moved = file.orig.contains('/')
        ? file.orig.substring(0, file.orig.lastIndexOf('/')) != file.dir
        : file.dir.isNotEmpty;
    final note = [
      if (flat && file.dir.isNotEmpty) file.dir,
      if (from != null) '← ${flat && moved ? file.orig : from}',
    ].join('  ');
    return InkWell(
      onTap: () => _open(file),
      child: SizedBox(
        height: 40,
        child: Padding(
          padding: EdgeInsets.only(
            left: 8 + _arrowBox + 4 + depth * _treeIndent,
            right: 14,
          ),
          child: Row(
            children: [
              SizedBox(
                width: _arrowBox,
                child: Text(
                  _letter(kind),
                  textAlign: TextAlign.center,
                  style: mono(
                    Fs.small,
                    color: _letterColor(c, kind),
                  ).copyWith(fontWeight: Fw.bold),
                ),
              ),
              const SizedBox(width: 8),
              FileIconView(
                file.name,
                size: Ic.medium,
                dir: file.isDir,
                muted: deleted,
              ),
              const SizedBox(width: 8),
              // 文件名完整显示，放不下时先截淡色的目录和原名；文件名本身最多占四分之三
              Expanded(
                child: LayoutBuilder(
                  builder: (context, box) => Row(
                    children: [
                      ConstrainedBox(
                        constraints: BoxConstraints(
                          maxWidth: box.maxWidth * 0.75,
                        ),
                        child: Text(
                          file.name,
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: TextStyle(
                            fontSize: Fs.body,
                            color: deleted ? c.muted : c.text,
                            decoration: deleted
                                ? TextDecoration.lineThrough
                                : null,
                            decorationColor: c.muted,
                          ),
                        ),
                      ),
                      if (note.isNotEmpty)
                        Expanded(
                          child: Padding(
                            padding: const EdgeInsets.only(left: 10),
                            child: Text(
                              note,
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                              style: mono(Fs.small, color: c.faint),
                            ),
                          ),
                        ),
                    ],
                  ),
                ),
              ),
              const SizedBox(width: 16),
              _Stats(file: file, inList: true),
            ],
          ),
        ),
      ),
    );
  }
}

/// 按目录 / 平铺：两个字写出来的小分段，选中的是强调色淡底。
class _ModeSwitch extends StatelessWidget {
  const _ModeSwitch({required this.tree, required this.onChanged});

  final bool tree;
  final ValueChanged<bool> onChanged;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    Widget option(bool value, IconData icon, String label) {
      final on = tree == value;
      return Semantics(
        button: true,
        selected: on,
        child: GestureDetector(
          behavior: HitTestBehavior.opaque,
          onTap: on ? null : () => onChanged(value),
          child: AnimatedContainer(
            duration: const Duration(milliseconds: 150),
            padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
            decoration: BoxDecoration(
              color: on ? c.tint : Colors.transparent,
              borderRadius: BorderRadius.circular(Rd.button),
            ),
            child: Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                Icon(icon, size: Ic.inline, color: on ? c.accent : c.faint),
                const SizedBox(width: 4),
                Text(
                  label,
                  style: TextStyle(
                    fontSize: Fs.small,
                    fontWeight: Fw.medium,
                    color: on ? c.accent : c.muted,
                  ),
                ),
              ],
            ),
          ),
        ),
      );
    }

    return Container(
      padding: const EdgeInsets.all(2),
      decoration: BoxDecoration(
        color: c.fill,
        borderRadius: BorderRadius.circular(Rd.input),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          option(true, Icons.account_tree_rounded, context.l10n.gitTreeView),
          option(false, Icons.view_list_rounded, context.l10n.gitFlatView),
        ],
      ),
    );
  }
}

String _letter(ChangeKind kind) => switch (kind) {
  ChangeKind.conflict => 'U',
  ChangeKind.added => 'A',
  ChangeKind.untracked => '?',
  ChangeKind.deleted => 'D',
  ChangeKind.renamed => 'R',
  ChangeKind.modified => 'M',
};

Color _letterColor(PosseColors c, ChangeKind kind) => switch (kind) {
  ChangeKind.conflict || ChangeKind.deleted => c.danger,
  ChangeKind.added || ChangeKind.untracked => c.sage,
  _ => c.accent,
};

class _Stats extends StatelessWidget {
  const _Stats({required this.file, this.inList = false});

  final GitFileChange file;

  /// 列表里已经按组和状态字母分开了，不再写「新文件」。
  final bool inList;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    const tnum = [FontFeature.tabularFigures()];
    if (file.binary || (file.added == 0 && file.deleted == 0)) {
      final label = inList && file.isNewFile ? '' : file.stats(context.l10n);
      if (label.isEmpty) return const SizedBox.shrink();
      return Text(
        label,
        style: TextStyle(fontSize: Fs.small, color: c.faint),
      );
    }
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        if (file.added > 0)
          Text(
            '+${file.added}',
            style: TextStyle(
              fontSize: Fs.small,
              color: c.sage,
              fontFeatures: tnum,
            ),
          ),
        if (file.added > 0 && file.deleted > 0) const SizedBox(width: 6),
        if (file.deleted > 0)
          Text(
            '−${file.deleted}',
            style: TextStyle(
              fontSize: Fs.small,
              color: c.danger,
              fontFeatures: tnum,
            ),
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
    final c = PosseColors.of(context);
    final l = context.l10n;
    final chips = <(IconData, String, Color)>[
      if (status.ahead > 0)
        (Icons.arrow_upward_rounded, l.gitAhead(status.ahead), c.muted),
      if (status.behind > 0)
        (Icons.arrow_downward_rounded, l.gitBehind(status.behind), c.muted),
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
                  Icon(
                    Icons.call_split_rounded,
                    size: Ic.medium,
                    color: c.accent,
                  ),
                  const SizedBox(width: 8),
                  Expanded(
                    child: Text(
                      status.branch.isEmpty ? l.gitDetachedHead : status.branch,
                      overflow: TextOverflow.ellipsis,
                      style: mono(
                        Fs.body,
                        color: c.text,
                      ).copyWith(fontWeight: Fw.medium),
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
                        l.gitTracking(status.upstream),
                        style: TextStyle(fontSize: Fs.small, color: c.muted),
                      ),
                    for (final (icon, text, color) in chips)
                      Row(
                        mainAxisSize: MainAxisSize.min,
                        children: [
                          Icon(icon, size: Ic.inline, color: color),
                          const SizedBox(width: 4),
                          Text(
                            text,
                            style: TextStyle(fontSize: Fs.small, color: color),
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

/// 看一个文件的改动：和文件页一样整页打开、一样的顶栏和代码排版。
/// 左边是行号（删掉的行是旧文件的行号），再是上色的 +/−，段头写从第几行开始。
class DiffViewer extends StatefulWidget {
  const DiffViewer({
    super.key,
    required this.file,
    required this.load,
    this.onOpenFile,
  });

  final GitFileChange file;
  final Future<GitDiffView> Function() load;
  final VoidCallback? onOpenFile;

  @override
  State<DiffViewer> createState() => _DiffViewerState();
}

class _DiffViewerState extends State<DiffViewer> {
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
      if (!mounted) return;
      await routeSettled(context);
      if (mounted) setState(() => _diff = diff);
    } catch (error) {
      if (mounted) setState(() => _error = friendlyError(error, context.l10n));
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        titleSpacing: 0,
        title: FilePathTitle(widget.file.path),
        actions: [
          _Stats(file: widget.file),
          if (widget.onOpenFile != null)
            IconButton(
              tooltip: context.l10n.gitViewFullFile,
              onPressed: widget.onOpenFile,
              icon: const Icon(Icons.description_rounded, size: Ic.button),
            )
          else
            const SizedBox(width: 12),
          const SizedBox(width: 4),
        ],
      ),
      body: _body(),
    );
  }

  Widget _body() {
    final diff = _diff;
    if (_error != null) {
      return EmptyState(
        icon: Icons.error_outline_rounded,
        text: _error!,
        error: true,
      );
    }
    if (diff == null) return const Center(child: PosseLoader());
    final images = diff.images;
    if (images != null) return _ImageDiff(old: images.old, now: images.now);
    if (widget.file.binary) {
      return EmptyState(
        icon: Icons.insert_drive_file_rounded,
        text: context.l10n.gitBinaryDiff,
      );
    }
    final rows = parseDiffRows(diff.diff);
    if (rows.isEmpty) {
      return EmptyState(
        icon: Icons.notes_rounded,
        text: context.l10n.gitNoDifferences,
      );
    }
    return _DiffLines(
      rows: rows,
      language: languageForPath(widget.file.path),
      notice: diff.truncated ? CodeNotice(context.l10n.gitDiffTruncated) : null,
    );
  }
}

/// 图片的 diff：改之前在上、现在在下（手机竖着放不下左右对照），各标像素尺寸和文件大小，
/// 透明处垫棋盘格；点一张全屏看、可以双指放大。
class _ImageDiff extends StatelessWidget {
  const _ImageDiff({required this.old, required this.now});

  final ImageSide? old;
  final ImageSide? now;

  @override
  Widget build(BuildContext context) {
    return ListView(
      padding: const EdgeInsets.fromLTRB(16, 8, 16, 32),
      children: [
        _ImageSideView(
          label: context.l10n.gitImageBefore,
          side: old,
          missing: context.l10n.gitImageNewFile,
        ),
        const SizedBox(height: 20),
        _ImageSideView(
          label: context.l10n.gitImageNow,
          side: now,
          missing: context.l10n.gitDeleted,
        ),
      ],
    );
  }
}

class _ImageSideView extends StatefulWidget {
  const _ImageSideView({
    required this.label,
    required this.side,
    required this.missing,
  });

  final String label;
  final ImageSide? side;
  final String missing;

  @override
  State<_ImageSideView> createState() => _ImageSideViewState();
}

class _ImageSideViewState extends State<_ImageSideView> {
  MemoryImage? _image;
  ImageStream? _stream;
  late final _listener = ImageStreamListener((info, _) {
    if (mounted) {
      setState(() => _pixels = '${info.image.width} × ${info.image.height}');
    }
  });
  String? _pixels;

  @override
  void initState() {
    super.initState();
    final bytes = widget.side?.bytes;
    if (bytes != null) _image = MemoryImage(bytes);
  }

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    final image = _image;
    if (image == null || _stream != null) return;
    _stream = image.resolve(createLocalImageConfiguration(context))
      ..addListener(_listener);
  }

  @override
  void dispose() {
    _stream?.removeListener(_listener);
    super.dispose();
  }

  void _zoom(MemoryImage image) {
    Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => Scaffold(
          appBar: AppBar(title: Text(widget.label)),
          body: InteractiveViewer(
            maxScale: 8,
            child: Center(child: Image(image: image)),
          ),
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final side = widget.side;
    final image = _image;
    final meta = [?_pixels, if (side != null) formatBytes(side.size)];
    final Widget body;
    if (side == null) {
      body = _placeholder(c, widget.missing);
    } else if (image == null) {
      body = _placeholder(c, context.l10n.gitImageTooLarge);
    } else {
      body = GestureDetector(
        onTap: () => _zoom(image),
        child: ClipRRect(
          borderRadius: BorderRadius.circular(Rd.code),
          child: CustomPaint(
            painter: _Checkerboard(c),
            child: Image(
              image: image,
              errorBuilder: (_, _, _) =>
                  _placeholder(c, context.l10n.gitImageUndrawable),
            ),
          ),
        ),
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            Text(
              widget.label,
              style: TextStyle(
                fontSize: Fs.small,
                fontWeight: Fw.medium,
                color: c.text,
              ),
            ),
            const SizedBox(width: 10),
            Expanded(
              child: Text(
                meta.join('    '),
                style: TextStyle(fontSize: Fs.small, color: c.muted),
              ),
            ),
          ],
        ),
        const SizedBox(height: 8),
        Center(child: body),
      ],
    );
  }

  Widget _placeholder(PosseColors c, String text) => Container(
    height: 120,
    alignment: Alignment.center,
    decoration: BoxDecoration(
      color: c.fill,
      borderRadius: BorderRadius.circular(Rd.code),
    ),
    child: Text(
      text,
      style: TextStyle(fontSize: Fs.small, color: c.faint),
    ),
  );
}

/// 透明图片底下的棋盘格，看得出哪里是透明的。
class _Checkerboard extends CustomPainter {
  _Checkerboard(this.c);

  final PosseColors c;

  @override
  void paint(Canvas canvas, Size size) {
    const cell = 8.0;
    canvas.drawRect(Offset.zero & size, Paint()..color = c.card);
    final dark = Paint()..color = c.fill;
    for (var y = 0.0; y < size.height; y += cell) {
      for (
        var x = ((y / cell).floor().isOdd ? cell : 0.0);
        x < size.width;
        x += cell * 2
      ) {
        canvas.drawRect(Rect.fromLTWH(x, y, cell, cell), dark);
      }
    }
  }

  @override
  bool shouldRepaint(_Checkerboard old) => old.c != c;
}

/// diff 的两边各自接成一段代码着色（跨行的注释、字符串才对），再按行对回每一行。
/// 旧的一边是上下文和删掉的行，新的一边是上下文和加上的行。
class _DiffLines extends StatelessWidget {
  const _DiffLines({required this.rows, this.language, this.notice});

  final List<DiffRow> rows;
  final String? language;
  final Widget? notice;

  @override
  Widget build(BuildContext context) {
    final old = <String>[], now = <String>[];
    final at = List<int>.filled(rows.length, -1);
    for (var i = 0; i < rows.length; i++) {
      final row = rows[i];
      if (row.kind == DiffKind.deleted || row.kind == DiffKind.context) {
        if (row.kind == DiffKind.deleted) at[i] = old.length;
        old.add(row.text);
      }
      if (row.kind == DiffKind.added || row.kind == DiffKind.context) {
        at[i] = now.length;
        now.add(row.text);
      }
    }
    return Highlighted(
      code: old.join('\n'),
      language: language,
      builder: (context, oldHl) => Highlighted(
        code: now.join('\n'),
        language: language,
        builder: (context, nowHl) {
          List<Seg>? segs(int i) {
            final side = rows[i].kind == DiffKind.deleted ? oldHl : nowHl;
            final n = at[i];
            return side == null || n < 0 || n >= side.length ? null : side[n];
          }

          return _lines(context, segs);
        },
      ),
    );
  }

  Widget _lines(BuildContext context, List<Seg>? Function(int i) segs) {
    final c = PosseColors.of(context);
    final style = codeStyle(c);
    final faint = style.copyWith(color: c.faint);
    var widest = 1;
    for (final row in rows) {
      final n = row.number;
      if (n != null && row.kind != DiffKind.hunk) {
        widest = widest < '$n'.length ? '$n'.length : widest;
      }
    }
    final alpha = c.dark ? 0.16 : 0.10;
    var columns = 0;
    for (final row in rows) {
      columns = math.max(columns, codeColumns(row.text));
    }

    Widget line(int index) {
      final row = rows[index];
      final newline = index < rows.length - 1 ? '\n' : '';
      final hl = segs(index);
      final (bg, sign, signColor) = switch (row.kind) {
        DiffKind.added => (c.sage.withValues(alpha: alpha), '+', c.sage),
        DiffKind.deleted => (c.danger.withValues(alpha: alpha), '−', c.danger),
        _ => (null, ' ', c.faint),
      };
      if (row.kind == DiffKind.hunk) {
        final at = row.number == null
            ? ''
            : context.l10n.gitHunkLine(row.number!);
        final label = [at, row.text].where((s) => s.isNotEmpty).join('  ');
        return Container(
          color: c.fill,
          margin: const EdgeInsets.only(top: 4),
          padding: const EdgeInsets.fromLTRB(8, 2, 16, 2),
          child: SelectionContainer.disabled(
            child: Text(
              label.isEmpty ? '⋯' : label,
              softWrap: false,
              style: faint.copyWith(color: c.muted),
            ),
          ),
        );
      }
      final number = row.number == null ? '' : '${row.number}';
      return Container(
        color: bg,
        padding: const EdgeInsets.only(left: 8, right: 16),
        child: Row(
          children: [
            SelectionContainer.disabled(
              child: Text.rich(
                TextSpan(
                  style: faint,
                  children: [
                    TextSpan(text: '${number.padLeft(widest)} '),
                    TextSpan(
                      text: sign,
                      style: TextStyle(color: signColor),
                    ),
                  ],
                ),
              ),
            ),
            const SizedBox(width: codeGutterGap - 6),
            Expanded(
              child: Text.rich(
                TextSpan(
                  children: [
                    if (hl != null && hl.isNotEmpty)
                      ...lineSpans(hl, c)
                    else
                      TextSpan(text: row.text),
                    TextSpan(text: newline),
                  ],
                ),
                maxLines: 1,
                softWrap: false,
                style: row.kind == DiffKind.note
                    ? style.copyWith(color: c.muted)
                    : style,
              ),
            ),
          ],
        ),
      );
    }

    return SelectionArea(
      child: CodeLines(
        // 行号、空格和加减号，再加左右边距和行号后面的空
        columns: widest + 2 + columns,
        extra: 8 + 16 + codeGutterGap - 6,
        header: [?notice],
        itemCount: rows.length,
        itemBuilder: (context, i) => line(i),
      ),
    );
  }
}
