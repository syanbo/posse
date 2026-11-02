import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:math' as math;
import 'dart:typed_data';

import 'package:flutter/material.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../protocol/rpc.dart';
import '../ui/file_icons.dart';
import '../ui/code.dart';
import '../ui/highlight.dart';
import '../ui/loading.dart';
import '../ui/media_pager.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../workspace/models.dart';
import '../workspace/workspace.dart';
import 'markdown_doc.dart';
import 'save.dart';
import 'video.dart';

/// 目录里的一项。路径都相对工作区根目录，用 / 分隔。
class FileEntry {
  const FileEntry({
    required this.path,
    required this.name,
    required this.dir,
    this.link = false,
  });

  final String path;
  final String name;
  final bool dir;

  /// 符号链接（指向目录的照样能展开）。旧电脑不给 isSymlink 时当普通文件。
  final bool link;
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
        link: item['isSymlink'] == true,
      ),
    );
  }
  out.sort((a, b) {
    if (a.dir != b.dir) return a.dir ? -1 : 1;
    return a.name.toLowerCase().compareTo(b.name.toLowerCase());
  });
  return out;
}

// 文件树每层缩进，照桌面端文件树
const _treeIndent = 18.0;

// 行首展开箭头占的宽，和箭头图标一样大
const _arrowBox = Ic.medium;

const _imageExts = {'png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'ico'};
const _binaryExts = {
  'avif', 'heic', 'mp3', 'pdf', 'zip', 'gz', 'tar', 'dmg', //
  'woff', 'woff2', 'ttf', 'otf', 'so', 'dylib', 'a', 'o', 'class', 'jar',
};

String _ext(String path) {
  final dot = path.lastIndexOf('.');
  final slash = path.lastIndexOf('/');
  return dot > slash ? path.substring(dot + 1).toLowerCase() : '';
}

/// 手机上能看的图片和视频：文件树里点开时和同目录的其他图片、视频一起左右翻。
bool isMediaPath(String path) {
  final ext = _ext(path);
  return _imageExts.contains(ext) || videoExts.contains(ext);
}

/// 单独打开的文件页（会话页、终端页顶栏的文件按钮）。工作区详情页里用的是不带顶栏的 FilesPane。
class FilesScreen extends StatelessWidget {
  const FilesScreen({
    super.key,
    required this.model,
    required this.worktree,
    required this.title,
  });

  final PosseAppModel model;
  final String worktree;
  final String title;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Scaffold(
      appBar: AppBar(
        titleSpacing: 0,
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(context.l10n.filesTitle),
            Text(
              title,
              overflow: TextOverflow.ellipsis,
              style: mono(
                Fs.small,
                color: c.muted,
              ).copyWith(fontWeight: Fw.body),
            ),
          ],
        ),
      ),
      body: FilesPane(model: model, worktree: worktree),
    );
  }
}

/// 把工作区里的文件下到手机（带进度），存到 [to]（不给时按格式定）。
Future<void> _download(
  BuildContext context,
  PosseAppModel model,
  String worktree,
  String path, {
  SaveTo? to,
}) async {
  final rpc = model.rpc;
  if (rpc == null) {
    ScaffoldMessenger.of(context)
        .showSnackBar(SnackBar(content: Text(context.l10n.errorNotConnected)));
    return;
  }
  await downloadAndSave(
    context,
    name: path,
    to: to,
    fetch: (cancelled, onProgress) => fetchFile(
      rpc,
      worktree: worktree,
      path: path,
      cancelled: cancelled,
      onProgress: onProgress,
    ),
  );
}

/// 工作区的文件：照桌面端文件栏的树，目录点一下就地展开，文件点开看内容、长按存到手机，顶上可以按路径找。只读。
class FilesPane extends StatefulWidget {
  const FilesPane({super.key, required this.model, required this.worktree});

  final PosseAppModel model;
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
      setState(() => _error = context.l10n.errorNotConnected);
      return;
    }
    setState(() => _loading.add(dir));
    try {
      final res = await rpc.call('files.readDir', {
        'worktree': widget.worktree,
        'relativePath': dir,
      });
      if (!mounted) return;
      await routeSettled(context);
      if (!mounted) return;
      setState(() {
        _children[dir] = parseDir(dir, res['value']);
        _error = null;
      });
    } catch (error) {
      if (mounted) setState(() => _error = friendlyError(error, context.l10n));
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
    final slash = path.lastIndexOf('/');
    final dir = slash < 0 ? '' : path.substring(0, slash);
    // 搜索结果的目录可能还没读过，那就只看这一个
    final siblings = isMediaPath(path)
        ? [
            for (final e in _children[dir] ?? const <FileEntry>[])
              if (!e.dir && isMediaPath(e.path)) e.path,
          ]
        : const <String>[];
    Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => FileViewer(
          model: widget.model,
          worktree: widget.worktree,
          path: path,
          siblings: siblings,
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
    final c = PosseColors.of(context);
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
                Icon(Icons.search_rounded, size: Ic.medium, color: c.faint),
                const SizedBox(width: 8),
                Text(
                  context.l10n.filesFindButton,
                  overflow: TextOverflow.ellipsis,
                  style: TextStyle(color: c.faint),
                ),
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
        action: TextButton(
          onPressed: () => _load(''),
          child: Text(context.l10n.commonRetry),
        ),
      );
    }
    if (rows.isEmpty) {
      return _loading.contains('')
          ? const Center(child: PosseLoader())
          : EmptyState(
              icon: Icons.folder_open_rounded,
              text: context.l10n.filesEmptyFolder,
            );
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

  Widget _row(PosseColors c, FileEntry e, int depth) {
    final open = _open.contains(e.path);
    return InkWell(
      onTap: () => e.dir ? _toggle(e) : _openFile(e.path),
      onLongPress: e.dir
          ? null
          : () => showSaveSheet(
              context,
              to: saveTarget(e.path),
              onSave: (to) => _download(
                context,
                widget.model,
                widget.worktree,
                e.path,
                to: to,
              ),
            ),
      child: Padding(
        padding: EdgeInsets.fromLTRB(12 + depth * _treeIndent, 0, 12, 0),
        child: SizedBox(
          height: 40,
          child: Row(
            children: [
              SizedBox(
                width: _arrowBox,
                child: e.dir
                    ? (_loading.contains(e.path)
                          ? const HexSpinner(size: Ic.glyph)
                          : AnimatedRotation(
                              turns: open ? 0.25 : 0,
                              duration: const Duration(milliseconds: 150),
                              child: Icon(
                                Icons.chevron_right_rounded,
                                size: Ic.medium,
                                color: c.faint,
                              ),
                            ))
                    : null,
              ),
              const SizedBox(width: 4),
              // 照桌面文件树：按类型的图标，Xcode 的包目录也换成各自的图标
              FileIconView(
                e.name,
                size: Ic.medium,
                dir: e.dir,
                open: open,
                link: e.link,
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
    final c = PosseColors.of(context);
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
              decoration: InputDecoration(
                hintText: context.l10n.filesFindHint,
                prefixIcon: const Icon(Icons.search_rounded),
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
                  leading: FileIconView(path, size: Ic.medium),
                  title: Text(slash < 0 ? path : path.substring(slash + 1)),
                  subtitle: slash < 0
                      ? null
                      : Text(
                          path.substring(0, slash),
                          style: mono(Fs.small, color: c.muted),
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

/// 只读看一个文件：文本带行号，Markdown 默认渲染（可切到源码），图片直接显示，视频下载后播放。
class FileViewer extends StatefulWidget {
  const FileViewer({
    super.key,
    required this.model,
    required this.worktree,
    required this.path,
    this.siblings = const [],
  });

  final PosseAppModel model;
  final String worktree;
  final String path;

  /// 同一目录里的图片和视频（含 [path]），按文件树的顺序：打开的是图片或视频时左右滑切换。
  /// 空的或不含 [path] 时只看这一个。
  final List<String> siblings;

  @override
  State<FileViewer> createState() => _FileViewerState();
}

class _FileViewerState extends State<FileViewer> {
  String? _text;
  var _binary = false;
  var _truncated = false;
  String? _error;
  var _source = false;

  /// 左右翻的图片和视频，和当前看的是第几个。
  late final List<String> _pages = widget.siblings.contains(widget.path)
      ? widget.siblings
      : [widget.path];
  late var _index = _pages.indexOf(widget.path);

  /// 取到手机上的图片和下好的视频，按路径记：顶栏的存到手机用，翻回来还在。
  final _images = <String, Uint8List>{};
  final _videos = <String, File>{};

  String get _path => _pages[_index];

  bool get _markdown => {'md', 'markdown'}.contains(_ext(widget.path));

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    final rpc = widget.model.rpc;
    if (rpc == null) {
      setState(() => _error = context.l10n.errorNotConnected);
      return;
    }
    final ext = _ext(widget.path);
    // 图片和视频由翻页里的每一页自己取
    if (isMediaPath(widget.path)) return;
    try {
      if (_binaryExts.contains(ext)) {
        setState(() => _binary = true);
        return;
      }
      final res = await rpc.call('files.read', {
        'worktree': widget.worktree,
        'relativePath': widget.path,
      });
      if (!mounted) return;
      await routeSettled(context);
      if (!mounted) return;
      final content = res['content'] as String? ?? '';
      setState(() {
        // 读到 NUL 字节当二进制，不硬画乱码
        _binary = content.contains('\u0000');
        _text = content;
        _truncated = res['truncated'] == true;
      });
    } catch (error) {
      if (mounted) setState(() => _error = friendlyError(error, context.l10n));
    }
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Scaffold(
      appBar: AppBar(
        titleSpacing: 0,
        title: FilePathTitle(_path),
        actions: [
          if (_markdown && _text != null && !_binary)
            TextButton(
              style: TextButton.styleFrom(
                backgroundColor: Colors.transparent,
                foregroundColor: c.accent,
              ),
              onPressed: () => setState(() => _source = !_source),
              child: Text(
                _source ? context.l10n.filesPreview : context.l10n.filesSource,
              ),
            ),
          _saveButton(),
          const SizedBox(width: 4),
        ],
      ),
      body: _body(c),
    );
  }

  /// 存到手机：图片用已经取来的那份，视频等下完，其他文件现下。
  Widget _saveButton() {
    final path = _path;
    final to = saveTarget(path);
    final isVideo = videoExts.contains(_ext(path));
    final video = _videos[path];
    final image = _images[path];
    return Builder(
      builder: (button) => IconButton(
        tooltip: saveLabel(context.l10n, to),
        icon: Icon(saveIcon(to)),
        onPressed: isVideo && video == null
            ? null
            : () => video != null
                  ? saveToPhone(button, video, path)
                  : image != null
                  ? saveBytesToPhone(button, image, path)
                  : _download(button, widget.model, widget.worktree, path),
      ),
    );
  }

  Widget _body(PosseColors c) {
    if (_error != null) {
      return EmptyState(
        icon: Icons.error_outline_rounded,
        text: _error!,
        error: true,
      );
    }
    final rpc = widget.model.rpc;
    if (isMediaPath(widget.path) && rpc != null) {
      return MediaPager(
        count: _pages.length,
        initialIndex: _index,
        onChanged: (i) => setState(() => _index = i),
        builder: (context, i) {
          final path = _pages[i];
          if (videoExts.contains(_ext(path))) {
            return VideoView.file(
              key: ValueKey(path),
              rpc: rpc,
              worktree: widget.worktree,
              path: path,
              onReady: (file) {
                if (mounted) setState(() => _videos[path] = file);
              },
            );
          }
          return _ImageFile(
            key: ValueKey(path),
            rpc: rpc,
            worktree: widget.worktree,
            path: path,
            onReady: (bytes) {
              if (mounted) setState(() => _images[path] = bytes);
            },
          );
        },
      );
    }
    if (_binary) {
      return EmptyState(
        icon: Icons.insert_drive_file_rounded,
        text: context.l10n.filesBinary,
      );
    }
    final text = _text;
    if (text == null) return const Center(child: PosseLoader());
    final notice = _truncated ? CodeNotice(context.l10n.filesTruncated) : null;
    if (_markdown && !_source) {
      return SelectionArea(
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            ?notice,
            Expanded(
              child: MarkdownDoc(
                data: text,
                model: widget.model,
                worktree: widget.worktree,
                path: widget.path,
                openFile: (path) => Navigator.of(context).push(
                  MaterialPageRoute<void>(
                    builder: (_) => FileViewer(
                      model: widget.model,
                      worktree: widget.worktree,
                      path: path,
                    ),
                  ),
                ),
              ),
            ),
          ],
        ),
      );
    }
    return _CodeView(
      text: text,
      language: languageForPath(widget.path),
      notice: notice,
    );
  }
}

/// 工作区里的一张图片：经 `files.readPreview` 取来，双指放大。
class _ImageFile extends StatefulWidget {
  const _ImageFile({
    super.key,
    required this.rpc,
    required this.worktree,
    required this.path,
    required this.onReady,
  });

  final PhoneRpc rpc;
  final String worktree;
  final String path;
  final ValueChanged<Uint8List> onReady;

  @override
  State<_ImageFile> createState() => _ImageFileState();
}

class _ImageFileState extends State<_ImageFile> {
  Uint8List? _image;
  var _binary = false;
  String? _error;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final res = await widget.rpc.call('files.readPreview', {
        'worktree': widget.worktree,
        'relativePath': widget.path,
      });
      final content = res['content'];
      if (!mounted) return;
      if (res['isImage'] == true &&
          (content is String || content is Uint8List)) {
        final bytes = rpcBytes(content);
        setState(() => _image = bytes);
        widget.onReady(bytes);
      } else {
        setState(() => _binary = true);
      }
    } catch (error) {
      if (mounted) setState(() => _error = friendlyError(error, context.l10n));
    }
  }

  @override
  Widget build(BuildContext context) {
    if (_error != null) {
      return EmptyState(
        icon: Icons.error_outline_rounded,
        text: _error!,
        error: true,
      );
    }
    if (_binary) {
      return EmptyState(
        icon: Icons.insert_drive_file_rounded,
        text: context.l10n.filesBinary,
      );
    }
    final image = _image;
    if (image == null) return const Center(child: PosseLoader());
    return ZoomImage(child: Image.memory(image));
  }
}

/// 源码：左边行号，不折行，整块横向滚动，缩进对得齐。认得出语言时着色，太大的文件不着色。
class _CodeView extends StatefulWidget {
  const _CodeView({required this.text, this.language, this.notice});

  final String text;
  final String? language;
  final Widget? notice;

  @override
  State<_CodeView> createState() => _CodeViewState();
}

class _CodeViewState extends State<_CodeView> {
  late List<String> _lines;
  late String _code;
  late int _widest;

  @override
  void initState() {
    super.initState();
    _split();
  }

  @override
  void didUpdateWidget(_CodeView old) {
    super.didUpdateWidget(old);
    if (old.text != widget.text) _split();
  }

  void _split() {
    final lines = const LineSplitter().convert(widget.text);
    _lines = lines.isEmpty ? [''] : lines;
    _code = _lines.join('\n');
    _widest = _lines.fold(0, (n, line) => math.max(n, codeColumns(line)));
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final style = codeStyle(c);
    final faint = style.copyWith(color: c.faint);
    final gutter = '${_lines.length}'.length;
    final language = widget.language;
    final big = language != null && _code.length > highlightLimit;

    Widget row(int i, HlLines? hl) {
      final last = i == _lines.length - 1;
      final segs = hl != null && i < hl.length ? hl[i] : null;
      return Padding(
        padding: EdgeInsets.only(
          left: codePadding.left,
          right: codePadding.right,
        ),
        child: Row(
          children: [
            SelectionContainer.disabled(
              child: Text('${i + 1}'.padLeft(gutter), style: faint),
            ),
            const SizedBox(width: codeGutterGap),
            Expanded(
              child: Text.rich(
                TextSpan(
                  children: [
                    if (segs != null)
                      ...lineSpans(segs, c)
                    else
                      TextSpan(text: _lines[i]),
                    if (!last) const TextSpan(text: '\n'),
                  ],
                ),
                maxLines: 1,
                softWrap: false,
                style: style,
              ),
            ),
          ],
        ),
      );
    }

    return SelectionArea(
      child: Highlighted(
        code: _code,
        language: language,
        builder: (context, hl) => CodeLines(
          columns: gutter + _widest,
          extra: codePadding.horizontal + codeGutterGap,
          header: [
            ?widget.notice,
            if (big) CodeNotice(context.l10n.filesNoHighlight),
          ],
          itemCount: _lines.length,
          prototype: row(0, null),
          itemBuilder: (context, i) => row(i, hl),
        ),
      ),
    );
  }
}

/// 工作区路径（用户目录写成 ~），文件页的副标题用。
String worktreePath(PosseAppModel model, String worktree) {
  for (final row in model.worktrees) {
    if (row.id == worktree) return tildePath(row.path);
  }
  return '';
}

/// 会话页、终端页顶栏上的按钮：打开和项目详情一样的"改动 | 文件 | 历史"页，不离开会话也能看 git、文件和这个目录的会话历史。
class FilesButton extends StatelessWidget {
  const FilesButton({
    super.key,
    required this.model,
    required this.worktree,
    required this.title,
  });

  final PosseAppModel model;
  final String worktree;
  final String title;

  @override
  Widget build(BuildContext context) {
    return IconButton(
      tooltip: context.l10n.filesChangesAndFiles,
      icon: const Icon(Icons.folder_rounded),
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
