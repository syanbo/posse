import 'dart:typed_data';

import '../protocol/rpc.dart' show rpcBytes;

class GitFileChange {
  const GitFileChange({
    required this.path,
    required this.added,
    required this.deleted,
    required this.untracked,
    required this.conflict,
    required this.binary,
    this.orig = '',
    this.x = '',
    this.y = '',
  });

  final String path;

  /// 改名前的路径，没改名时为空。
  final String orig;

  /// git status 的两个状态字母：暂存区、工作区（`.` 是没变）。
  final String x;
  final String y;

  ChangeKind get kind {
    if (conflict) return ChangeKind.conflict;
    if (untracked) return ChangeKind.untracked;
    if (x == 'R' || y == 'R' || orig.isNotEmpty) return ChangeKind.renamed;
    if (x == 'D' || y == 'D') return ChangeKind.deleted;
    if (x == 'A') return ChangeKind.added;
    return ChangeKind.modified;
  }

  String get name {
    final slash = path.lastIndexOf('/');
    return slash < 0 ? path : path.substring(slash + 1);
  }

  String get dir {
    final slash = path.lastIndexOf('/');
    return slash < 0 ? '' : path.substring(0, slash);
  }

  final int added;
  final int deleted;
  final bool untracked;
  final bool conflict;
  final bool binary;

  /// 电脑会把两边的图带过来（git.diff 的 images），diff 页画图片对照。
  bool get isImage => RegExp(
    r'\.(png|jpe?g|gif|webp|bmp)$',
    caseSensitive: false,
  ).hasMatch(path);

  String get stats {
    if (isImage) return '图片';
    if (binary) return '二进制';
    if (untracked && added == 0 && deleted == 0) return '新文件';
    final parts = <String>[
      if (added > 0) '+$added',
      if (deleted > 0) '-$deleted',
    ];
    if (parts.isEmpty) return conflict ? '冲突' : '';
    return parts.join(' ');
  }
}

enum ChangeKind { conflict, modified, added, deleted, renamed, untracked }

/// 改动列表里的一行：目录（[file] 为空）或文件。目录的 [label] 是并起来的路径（只有一个子目录的中间层并成一行，照桌面端的 changeTree）。
class ChangeRow {
  const ChangeRow({
    required this.key,
    required this.label,
    required this.depth,
    this.file,
    this.count = 0,
  });

  /// 目录是它的完整路径，用来记折叠。
  final String key;
  final String label;
  final int depth;
  final GitFileChange? file;

  /// 目录下一共几个改动的文件。
  final int count;
}

class _Dir {
  final dirs = <String, _Dir>{};
  final files = <GitFileChange>[];

  int get count => files.length + dirs.values.fold(0, (n, d) => n + d.count);
}

/// 把改动按目录排成树再摊平成行，目录在前、各自按名字排；[folded] 里的目录不展开。
List<ChangeRow> changeTree(
  List<GitFileChange> files, {
  Set<String> folded = const {},
}) {
  final root = _Dir();
  for (final f in files) {
    var d = root;
    if (f.dir.isNotEmpty) {
      for (final part in f.dir.split('/')) {
        d = d.dirs.putIfAbsent(part, _Dir.new);
      }
    }
    d.files.add(f);
  }
  final out = <ChangeRow>[];
  void walk(_Dir d, String prefix, int depth) {
    final names = d.dirs.keys.toList()..sort(_byName);
    for (final name in names) {
      var label = name;
      var sub = d.dirs[name]!;
      while (sub.files.isEmpty && sub.dirs.length == 1) {
        final only = sub.dirs.keys.single;
        label = '$label/$only';
        sub = sub.dirs[only]!;
      }
      final key = prefix.isEmpty ? label : '$prefix/$label';
      out.add(
        ChangeRow(key: key, label: label, depth: depth, count: sub.count),
      );
      if (!folded.contains(key)) walk(sub, key, depth + 1);
    }
    final list = [...d.files]..sort((a, b) => _byName(a.name, b.name));
    for (final f in list) {
      out.add(ChangeRow(key: f.path, label: f.name, depth: depth, file: f));
    }
  }

  walk(root, '', 0);
  return out;
}

int _byName(String a, String b) => a.toLowerCase().compareTo(b.toLowerCase());

class GitStatusView {
  const GitStatusView({
    required this.branch,
    required this.upstream,
    required this.ahead,
    required this.behind,
    required this.operation,
    required this.files,
  });

  final String branch;
  final String upstream;
  final int ahead;
  final int behind;
  final String operation;
  final List<GitFileChange> files;

  String get headline {
    final name = branch.isEmpty ? '分离头指针' : branch;
    final bits = <String>[name];
    if (upstream.isNotEmpty) bits.add(upstream);
    if (ahead > 0) bits.add('领先 $ahead');
    if (behind > 0) bits.add('落后 $behind');
    if (operation.isNotEmpty) bits.add(operation);
    return bits.join('  ');
  }
}

class GitDiffView {
  const GitDiffView({
    required this.path,
    required this.diff,
    required this.truncated,
    this.images,
  });

  final String path;
  final String diff;
  final bool truncated;

  /// 图片的两边：改之前（HEAD）和现在（工作区）。不是图片或电脑上的 hive 太旧时为空。
  final ({ImageSide? old, ImageSide? now})? images;
}

/// 图片 diff 的一边。[bytes] 为空而 [size] 有值表示图太大没传过来。
class ImageSide {
  const ImageSide({required this.size, this.bytes});

  final int size;
  final Uint8List? bytes;
}

ImageSide? _imageSide(Object? raw) {
  if (raw is! Map) return null;
  final content = raw['content'];
  return ImageSide(
    size: _count(raw['size']),
    bytes: content is String || content is Uint8List ? rpcBytes(content) : null,
  );
}

int _count(Object? value) => value is num ? value.toInt() : 0;

GitStatusView parseGitStatus(Map<String, Object?> raw) {
  final files = <GitFileChange>[];
  final list = raw['files'];
  if (list is List) {
    for (final item in list) {
      if (item is! Map) continue;
      final path = item['path'];
      if (path is! String || path.isEmpty) continue;
      files.add(
        GitFileChange(
          path: path,
          added: _count(item['added']),
          deleted: _count(item['deleted']),
          untracked: item['untracked'] == true,
          conflict: item['conflict'] == true,
          binary: item['binary'] == true,
          orig: item['orig'] as String? ?? '',
          x: item['x'] as String? ?? '',
          y: item['y'] as String? ?? '',
        ),
      );
    }
  }
  return GitStatusView(
    branch: raw['branch'] as String? ?? '',
    upstream: raw['upstream'] as String? ?? '',
    ahead: _count(raw['ahead']),
    behind: _count(raw['behind']),
    operation: raw['operation'] as String? ?? '',
    files: files,
  );
}

GitDiffView parseGitDiff(Map<String, Object?> raw) {
  return GitDiffView(
    path: raw['path'] as String? ?? '',
    diff: raw['diff'] as String? ?? '',
    truncated: raw['truncated'] == true,
    images: switch (raw['images']) {
      final Map<Object?, Object?> m => (
        old: _imageSide(m['old']),
        now: _imageSide(m['new']),
      ),
      _ => null,
    },
  );
}

enum DiffKind { hunk, added, deleted, context, note }

/// diff 里的一行：段头的 [number] 是这一段在新文件里从第几行开始，[text] 是 git 给的函数名之类；
/// 删掉的行带旧文件的行号，其余带新文件的行号。
class DiffRow {
  const DiffRow(this.kind, this.text, [this.number]);

  final DiffKind kind;
  final String text;
  final int? number;
}

final _hunkHeader = RegExp(r'^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@ ?(.*)$');

/// 把 unified diff 拆成行。第一个段头之前的文件头（diff --git、index、---、+++）不要，标题里已经有路径。
List<DiffRow> parseDiffRows(String diff) {
  final out = <DiffRow>[];
  var old = 0, now = 0, inHunk = false, numbered = false;
  final lines = diff.split('\n');
  if (lines.isNotEmpty && lines.last.isEmpty) lines.removeLast();
  for (final line in lines) {
    if (line.startsWith('@@')) {
      // 认不出起始行号的段头照样分段，只是不标行号
      final m = _hunkHeader.firstMatch(line);
      inHunk = true;
      numbered = m != null;
      old = m == null ? 0 : int.parse(m[1]!);
      now = m == null ? 0 : int.parse(m[2]!);
      out.add(
        DiffRow(DiffKind.hunk, m?[3]!.trim() ?? '', numbered ? now : null),
      );
      continue;
    }
    if (!inHunk) continue;
    int? at(int n) => numbered ? n : null;
    if (line.startsWith('+')) {
      out.add(DiffRow(DiffKind.added, line.substring(1), at(now++)));
    } else if (line.startsWith('-')) {
      out.add(DiffRow(DiffKind.deleted, line.substring(1), at(old++)));
    } else if (line.startsWith(r'\')) {
      out.add(DiffRow(DiffKind.note, line.substring(1).trim()));
    } else {
      final text = line.isEmpty ? '' : line.substring(1);
      out.add(DiffRow(DiffKind.context, text, at(now)));
      now++;
      old++;
    }
  }
  return out;
}
