import 'package:flutter/widgets.dart';
import 'package:flutter_svg/flutter_svg.dart';

import 'line_icons.dart';
import 'theme.dart';

part 'file_icons_data.dart';

/// 文件按类型的图标，和桌面端 `internal/app/fileicons.go` 一致：图形、颜色和识别规则都由那边的表生成
/// （[FileKind]、`file_icons_data.dart`），这里只是查表的顺序。

/// 按文件名认类型，name 可以带目录。完整文件名先于扩展名：package-lock.json 是锁文件，不是 JSON。
FileKind fileKindOf(String name) {
  final base = _base(name).toLowerCase();
  if (_kindByName[base] case final k?) return k;
  for (final (prefix, suffix, kind) in _kindByAffix) {
    if (base.startsWith(prefix) && base.endsWith(suffix)) return kind;
  }
  return _kindByExt[_ext(base)] ?? FileKind.other;
}

/// Xcode 把一些目录当成一个整体（包），按扩展名认；其余目录是普通文件夹，返回空。
FileKind? dirKindOf(String name) {
  final ext = _ext(name);
  // 点开头的目录（Ruby 的 .bundle 配置目录）只是名字像，不是包
  if (ext.isEmpty || ext == name) return null;
  return _kindByDirExt[ext.toLowerCase()];
}

/// 同 Go 的 filepath.Ext：最后一个点起的部分，点开头的名字（.gitignore）整个算扩展名。
String _ext(String base) {
  final i = base.lastIndexOf('.');
  return i < 0 ? '' : base.substring(i);
}

String _base(String path) {
  final i = path.lastIndexOf('/');
  return i < 0 ? path : path.substring(i + 1);
}

/// 类型的颜色：数据色按桌面 palette.data 的顺序，-1 是次要色。
Color fileKindColor(PosseColors c, FileKind kind) => switch (kind.color) {
  0 => c.accent,
  1 => c.sky,
  2 => c.teal,
  3 => c.plum,
  4 => c.gold,
  5 => c.cyan,
  _ => c.muted,
};

/// 文件或目录的图标。目录是强调色的文件夹（展开时是打开的文件夹），Xcode 的包目录用各自的图标；
/// 符号链接照桌面不按类型，是带拐弯箭头的文件夹（强调色）和纸（次要色）。
class FileIconView extends StatelessWidget {
  const FileIconView(
    this.name, {
    super.key,
    required this.size,
    this.dir = false,
    this.open = false,
    this.link = false,
  });

  final String name;
  final double size;
  final bool dir;
  final bool open;
  final bool link;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    if (link) {
      return SvgPicture.string(
        dir ? _folderLinkSvg : _fileLinkSvg,
        width: size,
        height: size,
        theme: SvgTheme(currentColor: dir ? c.accent : c.muted),
      );
    }
    final kind = dir ? dirKindOf(name) : fileKindOf(name);
    if (kind == null) {
      return LineIconView(
        open ? LineIcon.folderOpen : LineIcon.folder,
        size: size,
        color: c.accent,
      );
    }
    return SvgPicture.string(
      kind.svg,
      width: size,
      height: size,
      theme: SvgTheme(currentColor: fileKindColor(c, kind)),
    );
  }
}
