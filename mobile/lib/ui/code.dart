import 'package:flutter/material.dart';

import 'theme.dart';

/// 看源码和看 diff 共用的排版：等宽 12.5、行高 1.6，左边淡色行号，两页对得上。
TextStyle codeStyle(HiveColors c) => mono(12.5, height: 1.6, color: c.text);

/// 代码块的边距：左边留给行号的空少一些。
const codePadding = EdgeInsets.fromLTRB(8, 8, 16, 0);

/// 行号和代码之间的空。
const codeGutterGap = 14.0;

/// 内容上面的一行提示，比如文件太大只显示了一部分。
class CodeNotice extends StatelessWidget {
  const CodeNotice(this.text, {super.key});

  final String text;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 8, 16, 0),
      child: Text(text, style: TextStyle(fontSize: 12, color: c.honey)),
    );
  }
}

/// 看文件、看 diff 的顶栏标题：文件名，下面一行是所在目录。
class FilePathTitle extends StatelessWidget {
  const FilePathTitle(this.path, {super.key});

  final String path;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final slash = path.lastIndexOf('/');
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          slash < 0 ? path : path.substring(slash + 1),
          overflow: TextOverflow.ellipsis,
        ),
        if (slash > 0)
          Text(
            path.substring(0, slash),
            overflow: TextOverflow.ellipsis,
            // 顶栏标题样式是 600，副标题要显式改回常规字重
            style: mono(
              12,
              color: c.muted,
            ).copyWith(fontWeight: FontWeight.w400),
          ),
      ],
    );
  }
}
