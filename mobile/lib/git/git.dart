import 'package:flutter/cupertino.dart';
import 'package:flutter/material.dart';

import '../protocol/rpc.dart';
import '../ui/code.dart';
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

class GitPane extends StatefulWidget {
  const GitPane({
    super.key,
    required this.loadStatus,
    required this.loadDiff,
    this.onOpenFile,
  });

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
    if (_loading) return const Center(child: CircularProgressIndicator());
    if (_error != null && status == null) {
      return EmptyState(
        icon: Icons.cloud_off_rounded,
        text: _error!,
        error: true,
        action: TextButton(onPressed: _reload, child: const Text('重试')),
      );
    }
    final c = HiveColors.of(context);
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
            else ...[
              _listHeader(c, files.length),
              for (final (kind, label) in _groups)
                if (files.any((f) => _groupOf(f) == kind))
                  _group(
                    context,
                    kind,
                    label,
                    files.where((f) => _groupOf(f) == kind).toList(),
                  ),
            ],
            const SizedBox(height: 32),
          ],
        ),
      ],
    );
  }

  /// 照桌面端分组：冲突、改动、未跟踪。
  static const _groups = [
    (ChangeKind.conflict, '冲突'),
    (ChangeKind.modified, '改动'),
    (ChangeKind.untracked, '未跟踪'),
  ];

  static ChangeKind _groupOf(GitFileChange f) => switch (f.kind) {
    ChangeKind.conflict => ChangeKind.conflict,
    ChangeKind.untracked => ChangeKind.untracked,
    _ => ChangeKind.modified,
  };

  Widget _listHeader(HiveColors c, int count) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(20, 0, 16, 8),
      child: Row(
        children: [
          Expanded(
            child: Text(
              '$count 个文件有改动',
              style: TextStyle(fontSize: Fs.meta, color: c.muted),
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
    final c = HiveColors.of(context);
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
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 0, 16, 12),
      child: Container(
        decoration: BoxDecoration(
          color: c.card,
          borderRadius: BorderRadius.circular(Rd.card),
        ),
        clipBehavior: Clip.antiAlias,
        child: Column(
          children: [
            _foldRow(
              c,
              open: !folded,
              onTap: () => _fold('$kind'),
              depth: 0,
              child: Row(
                children: [
                  Text(
                    label,
                    style: TextStyle(
                      fontSize: Fs.meta,
                      fontWeight: FontWeight.w500,
                      color: kind == ChangeKind.conflict ? c.danger : c.text,
                    ),
                  ),
                  const SizedBox(width: 8),
                  Text(
                    '${files.length}',
                    style: TextStyle(fontSize: 12, color: c.faint),
                  ),
                ],
              ),
            ),
            if (!folded)
              for (final row in rows)
                row.file == null
                    ? _dirRow(c, kind, row)
                    : _fileRow(c, row.file!, row.depth + 1, flat: !_tree),
            if (!folded) const SizedBox(height: 4),
          ],
        ),
      ),
    );
  }

  void _fold(String key) => setState(() {
    if (!_folded.remove(key)) _folded.add(key);
  });

  /// 带展开箭头的一行：组标题和目录共用。
  Widget _foldRow(
    HiveColors c, {
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
          padding: EdgeInsets.only(left: 8 + depth * 16.0, right: 14),
          child: Row(
            children: [
              AnimatedRotation(
                turns: open ? 0.25 : 0,
                duration: const Duration(milliseconds: 150),
                child: Icon(
                  Icons.chevron_right_rounded,
                  size: 18,
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

  Widget _dirRow(HiveColors c, ChangeKind kind, ChangeRow row) {
    final key = '$kind:${row.key}';
    final open = !_folded.contains(key);
    return _foldRow(
      c,
      open: open,
      onTap: () => _fold(key),
      depth: row.depth + 1,
      child: Row(
        children: [
          Icon(
            open ? Icons.folder_open_outlined : Icons.folder_outlined,
            size: 18,
            color: c.accent,
          ),
          const SizedBox(width: 8),
          Flexible(
            child: Text(
              row.label,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: mono(13.5, color: c.text),
            ),
          ),
          const SizedBox(width: 8),
          Text('${row.count}', style: TextStyle(fontSize: 12, color: c.faint)),
        ],
      ),
    );
  }

  Widget _fileRow(
    HiveColors c,
    GitFileChange file,
    int depth, {
    required bool flat,
  }) {
    final kind = file.kind;
    final deleted = kind == ChangeKind.deleted;
    final from = kind == ChangeKind.renamed && file.orig.isNotEmpty
        ? file.orig.substring(file.orig.lastIndexOf('/') + 1)
        : null;
    // 平铺时目录和改名的原名都要写；原名改到别的目录时写全路径
    final note = [
      if (flat && file.dir.isNotEmpty) file.dir,
      if (from != null) '← ${flat ? file.orig : from}',
    ].join('  ');
    return InkWell(
      onTap: () => _open(file),
      child: SizedBox(
        height: 40,
        child: Padding(
          // 和目录的文件夹图标对齐：箭头 18 加间距 4
          padding: EdgeInsets.only(left: 30 + depth * 16.0, right: 14),
          child: Row(
            children: [
              SizedBox(
                width: 18,
                child: Text(
                  _letter(kind),
                  textAlign: TextAlign.center,
                  style: mono(
                    12.5,
                    color: _letterColor(c, kind),
                  ).copyWith(fontWeight: FontWeight.w600),
                ),
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
                              style: mono(12, color: c.faint),
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
    final c = HiveColors.of(context);
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
            padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 5),
            decoration: BoxDecoration(
              color: on ? c.tint : Colors.transparent,
              borderRadius: BorderRadius.circular(Rd.button),
            ),
            child: Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                Icon(icon, size: 15, color: on ? c.accent : c.faint),
                const SizedBox(width: 4),
                Text(
                  label,
                  style: TextStyle(
                    fontSize: 12.5,
                    fontWeight: FontWeight.w500,
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
        borderRadius: BorderRadius.circular(Rd.button + 2),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          option(true, Icons.account_tree_outlined, '按目录'),
          option(false, Icons.view_list_rounded, '平铺'),
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

Color _letterColor(HiveColors c, ChangeKind kind) => switch (kind) {
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
    final c = HiveColors.of(context);
    const tnum = [FontFeature.tabularFigures()];
    if (file.binary || (file.added == 0 && file.deleted == 0)) {
      final label = inList && file.stats == '新文件' ? '' : file.stats;
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
      if (mounted) setState(() => _diff = diff);
    } catch (error) {
      if (mounted) setState(() => _error = '$error');
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
              tooltip: '查看完整文件',
              onPressed: widget.onOpenFile,
              icon: const Icon(Icons.description_outlined, size: 22),
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
    if (diff == null) return const Center(child: CircularProgressIndicator());
    final images = diff.images;
    if (images != null) return _ImageDiff(old: images.old, now: images.now);
    if (widget.file.binary) {
      return const EmptyState(
        icon: Icons.insert_drive_file_outlined,
        text: '二进制文件，手机上看不了差异',
      );
    }
    final rows = parseDiffRows(diff.diff);
    if (rows.isEmpty) {
      return const EmptyState(icon: Icons.notes_rounded, text: '没有差异');
    }
    return _DiffLines(
      rows: rows,
      notice: diff.truncated ? const CodeNotice('差异太长，只显示前面一部分') : null,
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
        _ImageSideView(label: '改之前', side: old, missing: '新文件，之前没有'),
        const SizedBox(height: 20),
        _ImageSideView(label: '现在', side: now, missing: '已删除'),
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
    final c = HiveColors.of(context);
    final side = widget.side;
    final image = _image;
    final meta = [?_pixels, if (side != null) formatBytes(side.size)];
    final Widget body;
    if (side == null) {
      body = _placeholder(c, widget.missing);
    } else if (image == null) {
      body = _placeholder(c, '图片超过 10 MB，手机上不显示');
    } else {
      body = GestureDetector(
        onTap: () => _zoom(image),
        child: ClipRRect(
          borderRadius: BorderRadius.circular(Rd.code),
          child: CustomPaint(
            painter: _Checkerboard(c),
            child: Image(
              image: image,
              errorBuilder: (_, _, _) => _placeholder(c, '这张图手机上画不出来'),
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
                fontSize: Fs.meta,
                fontWeight: FontWeight.w500,
                color: c.text,
              ),
            ),
            const SizedBox(width: 10),
            Expanded(
              child: Text(
                meta.join('    '),
                style: TextStyle(fontSize: 12, color: c.faint),
              ),
            ),
          ],
        ),
        const SizedBox(height: 8),
        Center(child: body),
      ],
    );
  }

  Widget _placeholder(HiveColors c, String text) => Container(
    height: 120,
    alignment: Alignment.center,
    decoration: BoxDecoration(
      color: c.fill,
      borderRadius: BorderRadius.circular(Rd.code),
    ),
    child: Text(
      text,
      style: TextStyle(fontSize: Fs.meta, color: c.faint),
    ),
  );
}

/// 透明图片底下的棋盘格，看得出哪里是透明的。
class _Checkerboard extends CustomPainter {
  _Checkerboard(this.c);

  final HiveColors c;

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

class _DiffLines extends StatelessWidget {
  const _DiffLines({required this.rows, this.notice});

  final List<DiffRow> rows;
  final Widget? notice;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
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
    final screen = MediaQuery.sizeOf(context).width;

    Widget line(DiffRow row) {
      final (bg, sign, signColor) = switch (row.kind) {
        DiffKind.added => (c.sage.withValues(alpha: alpha), '+', c.sage),
        DiffKind.deleted => (c.danger.withValues(alpha: alpha), '−', c.danger),
        _ => (null, ' ', c.faint),
      };
      if (row.kind == DiffKind.hunk) {
        final at = row.number == null ? '' : '第 ${row.number} 行';
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
          mainAxisSize: MainAxisSize.min,
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
            Text(
              row.text.isEmpty ? ' ' : row.text,
              softWrap: false,
              style: row.kind == DiffKind.note
                  ? style.copyWith(color: c.muted)
                  : style,
            ),
          ],
        ),
      );
    }

    return SelectionArea(
      child: ListView(
        padding: const EdgeInsets.only(bottom: 32),
        children: [
          ?notice,
          SingleChildScrollView(
            scrollDirection: Axis.horizontal,
            padding: const EdgeInsets.only(top: 8),
            // 每行的底色都铺到最长那一行，横着滑过去右边是齐的
            child: ConstrainedBox(
              constraints: BoxConstraints(minWidth: screen),
              child: IntrinsicWidth(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [for (final row in rows) line(row)],
                ),
              ),
            ),
          ),
        ],
      ),
    );
  }
}
