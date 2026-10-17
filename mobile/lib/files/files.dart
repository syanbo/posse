import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter/material.dart';

import '../app_state.dart';
import '../chat/markdown_view.dart';
import '../protocol/rpc.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../workspace/models.dart';
import '../workspace/workspace.dart';

/// 目录里的一项。路径都相对工作区根目录，用 / 分隔。
class FileEntry {
  const FileEntry({required this.path, required this.name, required this.dir});

  final String path;
  final String name;
  final bool dir;
}

/// files.readDir 的结果：目录在前，各按名字排。
List<FileEntry> parseDir(String parent, Object? raw) {
  final out = <FileEntry>[];
  if (raw is! List) return out;
  for (final item in raw) {
    if (item is! Map) continue;
    final name = item['name'];
    if (name is! String || name.isEmpty) continue;
    out.add(
      FileEntry(
        path: parent.isEmpty ? name : '$parent/$name',
        name: name,
        dir: item['isDirectory'] == true,
      ),
    );
  }
  out.sort((a, b) {
    if (a.dir != b.dir) return a.dir ? -1 : 1;
    return a.name.toLowerCase().compareTo(b.name.toLowerCase());
  });
  return out;
}

const _imageExts = {'png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'ico'};
const _binaryExts = {
  'avif', 'heic', 'mov', 'mp3', 'mp4', 'pdf', 'zip', 'gz', 'tar', 'dmg', //
  'woff', 'woff2', 'ttf', 'otf', 'so', 'dylib', 'a', 'o', 'class', 'jar',
};

String _ext(String path) {
  final dot = path.lastIndexOf('.');
  final slash = path.lastIndexOf('/');
  return dot > slash ? path.substring(dot + 1).toLowerCase() : '';
}

/// 单独打开的文件页（会话页、终端页顶栏的文件按钮）。工作区详情页里用的是不带顶栏的 FilesPane。
class FilesScreen extends StatelessWidget {
  const FilesScreen({
    super.key,
    required this.model,
    required this.worktree,
    required this.title,
  });

  final HiveAppModel model;
  final String worktree;
  final String title;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    return Scaffold(
      appBar: AppBar(
        titleSpacing: 0,
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text('文件'),
            Text(
              title,
              overflow: TextOverflow.ellipsis,
              style: mono(
                12,
                color: c.muted,
              ).copyWith(fontWeight: FontWeight.w400),
            ),
          ],
        ),
      ),
      body: FilesPane(model: model, worktree: worktree),
    );
  }
}

/// 工作区的文件：照桌面端文件栏的树，目录点一下就地展开，文件点开看内容，顶上可以按路径找。只读。
class FilesPane extends StatefulWidget {
  const FilesPane({super.key, required this.model, required this.worktree});

  final HiveAppModel model;
  final String worktree;

  @override
  State<FilesPane> createState() => _FilesPaneState();
}

class _FilesPaneState extends State<FilesPane> {
  final _children = <String, List<FileEntry>>{};
  final _open = <String>{};
  final _loading = <String>{};
  String? _error;

  @override
  void initState() {
    super.initState();
    _load('');
  }

  Future<void> _load(String dir) async {
    final rpc = widget.model.rpc;
    if (rpc == null) {
      setState(() => _error = '还没连上电脑');
      return;
    }
    setState(() => _loading.add(dir));
    try {
      final res = await rpc.call('files.readDir', {
        'worktree': widget.worktree,
        'relativePath': dir,
      });
      if (!mounted) return;
      setState(() {
        _children[dir] = parseDir(dir, res['value']);
        _error = null;
      });
    } catch (error) {
      if (mounted) setState(() => _error = '$error');
    } finally {
      if (mounted) setState(() => _loading.remove(dir));
    }
  }

  void _toggle(FileEntry e) {
    setState(() {
      if (!_open.remove(e.path)) _open.add(e.path);
    });
    if (_open.contains(e.path) && !_children.containsKey(e.path)) {
      _load(e.path);
    }
  }

  void _openFile(String path) {
    Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => FileViewer(
          model: widget.model,
          worktree: widget.worktree,
          path: path,
        ),
      ),
    );
  }

  Future<void> _search() async {
    final rpc = widget.model.rpc;
    if (rpc == null) return;
    final path = await showModalBottomSheet<String>(
      context: context,
      isScrollControlled: true,
      showDragHandle: true,
      useSafeArea: true,
      builder: (_) => _SearchSheet(rpc: rpc, worktree: widget.worktree),
    );
    if (path != null && mounted) _openFile(path);
  }

  /// 展开的树按显示顺序摊平成行。
  List<(FileEntry, int)> _rows() {
    final out = <(FileEntry, int)>[];
    void walk(String dir, int depth) {
      for (final e in _children[dir] ?? const <FileEntry>[]) {
        out.add((e, depth));
        if (e.dir && _open.contains(e.path)) walk(e.path, depth + 1);
      }
    }

    walk('', 0);
    return out;
  }

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final rows = _rows();
    final search = Padding(
      padding: const EdgeInsets.fromLTRB(16, 4, 16, 8),
      child: Material(
        color: c.fill,
        borderRadius: BorderRadius.circular(Rd.input),
        child: InkWell(
          borderRadius: BorderRadius.circular(Rd.input),
          onTap: _search,
          child: SizedBox(
            height: 40,
            child: Row(
              children: [
                const SizedBox(width: 12),
                Icon(Icons.search_rounded, size: 20, color: c.faint),
                const SizedBox(width: 8),
                Text('按文件名或路径找', style: TextStyle(color: c.faint)),
              ],
            ),
          ),
        ),
      ),
    );
    if (_error != null && rows.isEmpty) {
      return EmptyState(
        icon: Icons.cloud_off_rounded,
        text: _error!,
        error: true,
        action: TextButton(onPressed: () => _load(''), child: const Text('重试')),
      );
    }
    if (rows.isEmpty) {
      return _loading.contains('')
          ? const Center(child: CircularProgressIndicator())
          : const EmptyState(icon: Icons.folder_open_rounded, text: '目录是空的');
    }
    return ListView.builder(
      padding: const EdgeInsets.only(bottom: 32),
      itemCount: rows.length + 1,
      itemBuilder: (context, i) {
        if (i == 0) return search;
        final (e, depth) = rows[i - 1];
        return _row(c, e, depth);
      },
    );
  }

  Widget _row(HiveColors c, FileEntry e, int depth) {
    final open = _open.contains(e.path);
    return InkWell(
      onTap: () => e.dir ? _toggle(e) : _openFile(e.path),
      child: Padding(
        // 尺寸照桌面端文件树：每层缩进 18
        padding: EdgeInsets.fromLTRB(12 + depth * 18.0, 0, 12, 0),
        child: SizedBox(
          height: 40,
          child: Row(
            children: [
              SizedBox(
                width: 18,
                child: e.dir
                    ? (_loading.contains(e.path)
                          ? const SizedBox.square(
                              dimension: 12,
                              child: CircularProgressIndicator(
                                strokeWidth: 1.5,
                              ),
                            )
                          : AnimatedRotation(
                              turns: open ? 0.25 : 0,
                              duration: const Duration(milliseconds: 150),
                              child: Icon(
                                Icons.chevron_right_rounded,
                                size: 18,
                                color: c.faint,
                              ),
                            ))
                    : null,
              ),
              const SizedBox(width: 4),
              Icon(
                e.dir
                    ? (open
                          ? Icons.folder_open_outlined
                          : Icons.folder_outlined)
                    : Icons.insert_drive_file_outlined,
                size: 20,
                color: e.dir ? c.accent : c.muted,
              ),
              const SizedBox(width: 10),
              Expanded(
                child: Text(
                  e.name,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: TextStyle(fontSize: Fs.body, color: c.text),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

/// 按路径模糊找文件，和桌面端的快速打开一样。
class _SearchSheet extends StatefulWidget {
  const _SearchSheet({required this.rpc, required this.worktree});

  final PhoneRpc rpc;
  final String worktree;

  @override
  State<_SearchSheet> createState() => _SearchSheetState();
}

class _SearchSheetState extends State<_SearchSheet> {
  final _query = TextEditingController();
  var _results = const <String>[];
  Timer? _debounce;
  var _seq = 0;

  @override
  void dispose() {
    _debounce?.cancel();
    _query.dispose();
    super.dispose();
  }

  void _changed(String text) {
    _debounce?.cancel();
    _debounce = Timer(const Duration(milliseconds: 200), () => _run(text));
  }

  Future<void> _run(String text) async {
    final seq = ++_seq;
    if (text.trim().isEmpty) {
      setState(() => _results = const []);
      return;
    }
    try {
      final res = await widget.rpc.call('files.searchPaths', {
        'worktree': widget.worktree,
        'query': text.trim(),
        'limit': 32,
      });
      if (!mounted || seq != _seq) return;
      final files = res['files'];
      setState(() {
        _results = [
          if (files is List)
            for (final f in files)
              if (f is Map && f['relativePath'] is String)
                f['relativePath'] as String,
        ];
      });
    } catch (_) {}
  }

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    return SizedBox(
      height: MediaQuery.sizeOf(context).height * 0.8,
      child: Column(
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
            child: TextField(
              controller: _query,
              autofocus: true,
              onChanged: _changed,
              textInputAction: TextInputAction.search,
              decoration: const InputDecoration(
                hintText: '文件名或路径',
                prefixIcon: Icon(Icons.search_rounded),
              ),
            ),
          ),
          Expanded(
            child: ListView.builder(
              itemCount: _results.length,
              itemBuilder: (context, i) {
                final path = _results[i];
                final slash = path.lastIndexOf('/');
                return RowTile(
                  chevron: false,
                  leading: Icon(
                    Icons.insert_drive_file_outlined,
                    size: 20,
                    color: c.muted,
                  ),
                  title: Text(slash < 0 ? path : path.substring(slash + 1)),
                  subtitle: slash < 0
                      ? null
                      : Text(
                          path.substring(0, slash),
                          style: mono(12, color: c.muted),
                        ),
                  onTap: () => Navigator.of(context).pop(path),
                );
              },
            ),
          ),
        ],
      ),
    );
  }
}

/// 只读看一个文件：文本带行号，Markdown 默认渲染（可切到源码），图片直接显示。
class FileViewer extends StatefulWidget {
  const FileViewer({
    super.key,
    required this.model,
    required this.worktree,
    required this.path,
  });

  final HiveAppModel model;
  final String worktree;
  final String path;

  @override
  State<FileViewer> createState() => _FileViewerState();
}

class _FileViewerState extends State<FileViewer> {
  String? _text;
  Uint8List? _image;
  var _binary = false;
  var _truncated = false;
  String? _error;
  var _source = false;

  bool get _markdown => {'md', 'markdown'}.contains(_ext(widget.path));

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    final rpc = widget.model.rpc;
    if (rpc == null) {
      setState(() => _error = '还没连上电脑');
      return;
    }
    final ext = _ext(widget.path);
    try {
      if (_imageExts.contains(ext)) {
        final res = await rpc.call('files.readPreview', {
          'worktree': widget.worktree,
          'relativePath': widget.path,
        });
        final content = res['content'];
        if (!mounted) return;
        setState(() {
          if (res['isImage'] == true && content is String) {
            _image = base64Decode(content);
          } else {
            _binary = true;
          }
        });
        return;
      }
      if (_binaryExts.contains(ext)) {
        setState(() => _binary = true);
        return;
      }
      final res = await rpc.call('files.read', {
        'worktree': widget.worktree,
        'relativePath': widget.path,
      });
      if (!mounted) return;
      final content = res['content'] as String? ?? '';
      setState(() {
        // 读到 NUL 字节当二进制，不硬画乱码
        _binary = content.contains('\u0000');
        _text = content;
        _truncated = res['truncated'] == true;
      });
    } catch (error) {
      if (mounted) setState(() => _error = '$error');
    }
  }

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final slash = widget.path.lastIndexOf('/');
    final name = slash < 0 ? widget.path : widget.path.substring(slash + 1);
    return Scaffold(
      appBar: AppBar(
        titleSpacing: 0,
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(name, overflow: TextOverflow.ellipsis),
            if (slash > 0)
              Text(
                widget.path.substring(0, slash),
                overflow: TextOverflow.ellipsis,
                style: mono(
                  12,
                  color: c.muted,
                ).copyWith(fontWeight: FontWeight.w400),
              ),
          ],
        ),
        actions: [
          if (_markdown && _text != null && !_binary)
            TextButton(
              style: TextButton.styleFrom(
                backgroundColor: Colors.transparent,
                foregroundColor: c.accent,
              ),
              onPressed: () => setState(() => _source = !_source),
              child: Text(_source ? '预览' : '源码'),
            ),
          const SizedBox(width: 4),
        ],
      ),
      body: _body(c),
    );
  }

  Widget _body(HiveColors c) {
    if (_error != null) {
      return EmptyState(
        icon: Icons.error_outline_rounded,
        text: _error!,
        error: true,
      );
    }
    if (_binary) {
      return const EmptyState(
        icon: Icons.insert_drive_file_outlined,
        text: '二进制文件，手机上看不了内容',
      );
    }
    final image = _image;
    if (image != null) {
      return InteractiveViewer(
        maxScale: 6,
        child: Center(child: Image.memory(image)),
      );
    }
    final text = _text;
    if (text == null) return const Center(child: CircularProgressIndicator());
    final notice = _truncated
        ? Padding(
            padding: const EdgeInsets.fromLTRB(16, 8, 16, 0),
            child: Text(
              '文件太大，只显示前 512 KB',
              style: TextStyle(fontSize: 12, color: c.honey),
            ),
          )
        : null;
    if (_markdown && !_source) {
      return SelectionArea(
        child: ListView(
          padding: const EdgeInsets.fromLTRB(16, 8, 16, 32),
          children: [
            ?notice,
            MarkdownView(
              text,
              style: TextStyle(fontSize: Fs.read, height: 1.6, color: c.text),
            ),
          ],
        ),
      );
    }
    return _CodeView(text: text, notice: notice);
  }
}

/// 源码：左边行号，不折行，整块横向滚动，缩进对得齐。
class _CodeView extends StatelessWidget {
  const _CodeView({required this.text, this.notice});

  final String text;
  final Widget? notice;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final lines = const LineSplitter().convert(text);
    final gutter = '${lines.length}'.length;
    final style = mono(12.5, height: 1.6, color: c.text);
    return SelectionArea(
      child: ListView(
        padding: const EdgeInsets.only(bottom: 32),
        children: [
          ?notice,
          SingleChildScrollView(
            scrollDirection: Axis.horizontal,
            padding: const EdgeInsets.fromLTRB(8, 8, 16, 0),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                SelectionContainer.disabled(
                  child: Text(
                    [
                      for (var i = 1; i <= lines.length; i++)
                        '$i'.padLeft(gutter),
                    ].join('\n'),
                    textAlign: TextAlign.right,
                    style: style.copyWith(color: c.faint),
                  ),
                ),
                const SizedBox(width: 14),
                Text(lines.join('\n'), softWrap: false, style: style),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

/// 工作区路径（用户目录写成 ~），文件页的副标题用。
String worktreePath(HiveAppModel model, String worktree) {
  for (final row in model.worktrees) {
    if (row.id == worktree) return tildePath(row.path);
  }
  return '';
}

/// 会话页、终端页顶栏上的按钮：打开和项目详情一样的"改动 | 文件"页，不离开会话也能看 git 和文件。
class FilesButton extends StatelessWidget {
  const FilesButton({
    super.key,
    required this.model,
    required this.worktree,
    required this.title,
  });

  final HiveAppModel model;
  final String worktree;
  final String title;

  @override
  Widget build(BuildContext context) {
    return IconButton(
      tooltip: '改动和文件',
      icon: const Icon(Icons.folder_outlined),
      onPressed: () {
        WorktreeRow? row;
        for (final r in model.worktrees) {
          if (r.id == worktree) row = r;
        }
        final nav = Navigator.of(context);
        if (row == null) {
          // 首页的列表还没回来时退回只看文件
          nav.push(
            MaterialPageRoute<void>(
              builder: (_) =>
                  FilesScreen(model: model, worktree: worktree, title: title),
            ),
          );
          return;
        }
        nav.push(
          MaterialPageRoute<void>(
            builder: (_) => TabScreen(model: model, worktree: row!),
          ),
        );
      },
    );
  }
}
