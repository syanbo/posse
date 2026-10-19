/// 终端里点到的链接在手机上怎么打开。纯计算，不碰终端控件。
sealed class LinkTarget {
  const LinkTarget();
}

/// 交给系统浏览器或邮件。
class OpenUrl extends LinkTarget {
  const OpenUrl(this.uri);

  final Uri uri;
}

/// 工作区里的文件，[path] 相对工作区根目录，交给文件页看。
class OpenFile extends LinkTarget {
  const OpenFile(this.path);

  final String path;
}

/// 电脑上的文件但不在这个工作区里：手机读不到（文件接口只认工作区里的相对路径）。
class NotInWorkspace extends LinkTarget {
  const NotInWorkspace(this.path);

  final String path;
}

const _webSchemes = {'http', 'https', 'mailto'};

/// [root] 是工作区在电脑上的路径，不知道时为 null。
/// 文件链接优先用 flterm 按终端当前目录解析出的绝对路径，没有时相对路径按工作区根目录算。
LinkTarget? resolveLink({
  Uri? uri,
  String? filePath,
  String? resolvedPath,
  String? root,
}) {
  if (uri != null) {
    final scheme = uri.scheme.toLowerCase();
    if (_webSchemes.contains(scheme)) return OpenUrl(uri);
    if (scheme != 'file') return null;
    return _file(Uri.decodeComponent(uri.path), root);
  }
  final path = resolvedPath ?? filePath;
  if (path == null || path.isEmpty) return null;
  return _file(path, root);
}

LinkTarget? _file(String path, String? root) {
  if (path.startsWith('~')) return NotInWorkspace(path);
  if (!path.startsWith('/')) {
    final rel = _normalize(path);
    return rel == null ? null : OpenFile(rel);
  }
  final abs = _normalize(path.substring(1));
  if (abs == null) return NotInWorkspace(path);
  final base = root == null ? null : _normalize(root.replaceFirst('/', ''));
  if (base == null || base.isEmpty) return NotInWorkspace(path);
  for (final candidate in _aliases(base)) {
    if (abs == candidate) return null;
    if (abs.startsWith('$candidate/')) {
      return OpenFile(abs.substring(candidate.length + 1));
    }
  }
  return NotInWorkspace(path);
}

/// macOS 上 /var、/tmp 是 /private 下的符号链接，终端报的目录常带着 /private。
List<String> _aliases(String base) {
  if (base.startsWith('private/')) return [base, base.substring(8)];
  if (base.startsWith('var/') || base.startsWith('tmp/')) {
    return [base, 'private/$base'];
  }
  return [base];
}

/// 去掉 `.`、空段，`..` 往上退；退出根目录时返回 null。结果不带开头的 `/`。
String? _normalize(String path) {
  final out = <String>[];
  for (final part in path.split('/')) {
    if (part.isEmpty || part == '.') continue;
    if (part == '..') {
      if (out.isEmpty) return null;
      out.removeLast();
      continue;
    }
    out.add(part);
  }
  return out.join('/');
}
