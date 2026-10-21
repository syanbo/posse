import 'dart:math' as math;

import 'package:flutter/material.dart';

import 'theme.dart';

/// 看源码和看 diff 共用的排版：等宽 12.5、行高 1.6，左边淡色行号，两页对得上。
TextStyle codeStyle(HiveColors c) => mono(12.5, height: 1.6, color: c.text);

/// 代码块的边距：左边留给行号的空少一些。
const codePadding = EdgeInsets.fromLTRB(8, 8, 16, 0);

/// 行号和代码之间的空。
const codeGutterGap = 14.0;

/// 估算一行等宽代码占几格：中日韩、全角和 emoji 按两格，tab 按四格。
/// 宁多勿少，少了行尾会被裁掉。
int codeColumns(String line) {
  var n = 0;
  for (final unit in line.codeUnits) {
    n += unit == 0x09 ? 4 : (unit >= 0x1100 ? 2 : 1);
  }
  return n;
}

/// 不折行的代码行：整块横向滚动，竖着只建看得见的行。
/// 整份文件排成一段文字的话，进页那一帧要排完几千行，动画会顿一下。
///
/// 每行是单独的文字，选中复制时行之间没有换行；[itemBuilder] 给除最后一行外的行
/// 末尾补上 `\n`，并用 `maxLines: 1` 只画第一行。
class CodeLines extends StatelessWidget {
  const CodeLines({
    super.key,
    required this.columns,
    required this.extra,
    required this.itemCount,
    required this.itemBuilder,
    this.header = const [],
    this.prototype,
  });

  /// 最长一行（含行号）的格数，按 [codeColumns] 算。
  final int columns;

  /// 格子以外的宽度：边距、行号和代码之间的空。
  final double extra;
  final int itemCount;
  final IndexedWidgetBuilder itemBuilder;

  /// 代码上面的提示，不跟着横向滚动。
  final List<Widget> header;

  /// 每行一样高时给一行做样子，滚动时不用逐行量高度。
  final Widget? prototype;

  @override
  Widget build(BuildContext context) {
    final cell = _cellWidth(
      codeStyle(HiveColors.of(context)),
      MediaQuery.textScalerOf(context),
    );
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        ...header,
        Expanded(
          child: LayoutBuilder(
            builder: (context, box) => SingleChildScrollView(
              scrollDirection: Axis.horizontal,
              child: SizedBox(
                // 每行都铺到最长那一行，底色横着滑过去右边是齐的
                width: math.max(box.maxWidth, extra + columns * cell),
                child: ListView.builder(
                  padding: const EdgeInsets.only(top: 8, bottom: 32),
                  itemCount: itemCount,
                  // 样子行也在树里，不排除的话复制时会多出一份第一行
                  prototypeItem: prototype == null
                      ? null
                      : SelectionContainer.disabled(child: prototype!),
                  itemBuilder: itemBuilder,
                ),
              ),
            ),
          ),
        ),
      ],
    );
  }
}

double _cellWidth(TextStyle style, TextScaler scaler) {
  final painter = TextPainter(
    text: TextSpan(text: '0000000000', style: style),
    textDirection: TextDirection.ltr,
    textScaler: scaler,
  )..layout();
  final width = painter.width / 10;
  painter.dispose();
  return width;
}

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
