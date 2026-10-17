class GitFileChange {
  const GitFileChange({
    required this.path,
    required this.added,
    required this.deleted,
    required this.untracked,
    required this.conflict,
    required this.binary,
  });

  final String path;
  final int added;
  final int deleted;
  final bool untracked;
  final bool conflict;
  final bool binary;

  String get stats {
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
  });

  final String path;
  final String diff;
  final bool truncated;
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
  );
}
