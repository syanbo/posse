import 'dart:async';

import 'package:flterm/flterm.dart' hide Key;
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../l10n/l10n.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'copy_chip.dart';

/// 选区上的「复制 / 全选」和两端把手。
///
/// 放在终端上面、自己不接空白处的点击，点空白仍由终端清掉选区。
/// 按下把手时记下手指和这一端差几格，拖的时候按这个差值走，圆点不在字上也不会一按就跳。
class SelectionChrome extends StatefulWidget {
  const SelectionChrome({
    super.key,
    required this.controller,
    required this.width,
    required this.height,
    required this.covered,
    required this.cellWidth,
    required this.cellHeight,
    required this.padLeft,
    required this.padTop,
    required this.lift,
    required this.pinch,
    required this.panX,
    required this.panY,
    required this.cropX,
  });

  final TerminalController controller;
  final double width;
  final double height;
  final double covered;
  final double cellWidth;
  final double cellHeight;
  final double padLeft;
  final double padTop;
  final double lift;
  final double pinch;
  final double panX;
  final double panY;
  final double cropX;

  @override
  State<SelectionChrome> createState() => _SelectionChromeState();
}

class _SelectionChromeState extends State<SelectionChrome> {
  var _selected = false;
  SelectionAnchor? _anchor;
  SelectionEnds? _ends;
  Position? _fixedEnd;
  var _originRow = 0;
  var _originCol = 0;
  int? _grabRow;
  int? _grabCol;
  Timer? _handleTimer;
  var _handleEdge = 0;
  Offset? _handleGlobal;

  @override
  void initState() {
    super.initState();
    widget.controller.addListener(_onController);
    _read();
  }

  @override
  void didUpdateWidget(SelectionChrome oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.controller == widget.controller) return;
    oldWidget.controller.removeListener(_onController);
    widget.controller.addListener(_onController);
    _read();
  }

  @override
  void dispose() {
    _stopHandleScroll();
    widget.controller.removeListener(_onController);
    super.dispose();
  }

  void _read() {
    _selected = widget.controller.hasSelection;
    _anchor = widget.controller.selectionAnchor;
    _ends = widget.controller.selectionEnds;
  }

  void _onController() {
    final anchor = widget.controller.selectionAnchor;
    final ends = widget.controller.selectionEnds;
    final selected = widget.controller.hasSelection;
    // 拖把手回滚时另一端会滚出屏幕，ends 变成 null；留着上一份，被拖的把手才不会被拆掉。
    final kept = ends ?? (_fixedEnd != null ? _ends : null);
    if (!mounted ||
        (selected == _selected && anchor == _anchor && kept == _ends)) {
      return;
    }
    setState(() {
      _selected = selected;
      _anchor = anchor;
      _ends = kept;
    });
  }

  /// 圆点在格子底下，触点往上移到这一端的字上。
  double get _fingerLift => widget.cellHeight * widget.pinch + 7;

  void _beginHandle(Position fixed, int row, int col) {
    _fixedEnd = fixed;
    _originRow = row;
    _originCol = col;
    _grabRow = null;
    _grabCol = null;
    _handleEdge = 0;
  }

  void _dragHandle(Offset global) {
    _handleGlobal = global;
    final box = context.findRenderObject();
    if (box is! RenderBox || !box.hasSize || _fixedEnd == null) return;
    final local = box.globalToLocal(global);
    final cell = viewCellAt(
      x: local.dx,
      y: local.dy,
      fingerLift: _fingerLift,
      cellWidth: widget.cellWidth,
      cellHeight: widget.cellHeight,
      padLeft: widget.padLeft,
      padTop: widget.padTop,
      lift: widget.lift,
      pinch: widget.pinch,
      panX: widget.panX,
      panY: widget.panY,
      cropX: widget.cropX,
    );
    _grabRow ??= cell.row - _originRow;
    _grabCol ??= cell.col - _originCol;
    final adjustedRow = cell.row - _grabRow!;
    final adjustedCol = cell.col - _grabCol!;
    final visible = widget.cellHeight > 0
        ? ((widget.height - widget.covered) / widget.cellHeight).floor()
        : 0;
    _handleEdge = adjustedRow < 0
        ? -1
        : (visible > 0 && adjustedRow >= visible ? 1 : 0);
    final row = _handleEdge < 0
        ? 0
        : _handleEdge > 0
        ? (visible > 0 ? visible - 1 : adjustedRow)
        : adjustedRow;
    widget.controller.moveSelectionEnd(
      fixed: _fixedEnd!,
      viewportCell: Position(row: row, col: adjustedCol),
    );
    if (_handleEdge != 0) {
      _handleTimer ??= Timer.periodic(const Duration(milliseconds: 50), (_) {
        if (!mounted || _handleEdge == 0 || _handleGlobal == null) return;
        widget.controller.nudgeViewport(_handleEdge);
        _dragHandle(_handleGlobal!);
      });
    } else {
      _stopHandleScroll();
    }
  }

  void _endHandle() {
    _fixedEnd = null;
    _handleGlobal = null;
    _handleEdge = 0;
    _grabRow = null;
    _grabCol = null;
    _stopHandleScroll();
    _onController();
  }

  void _stopHandleScroll() {
    _handleTimer?.cancel();
    _handleTimer = null;
  }

  @override
  Widget build(BuildContext context) {
    if (!_selected) return const SizedBox.shrink();
    // 选区两端都在屏幕外（段落比一屏长）时没有锚点，按钮放到最上面一行，仍然能复制。
    final anchor =
        _anchor ??
        SelectionAnchor(
          topRow: 0,
          leftCol: 0,
          rightCol: widget.cellWidth > 0
              ? (widget.width / widget.cellWidth).floor() - 1
              : 0,
        );
    final ends = _ends;
    final viewHeight = widget.height - widget.covered;
    return Stack(
      clipBehavior: Clip.none,
      fit: StackFit.expand,
      children: [
        if (ends != null) ...[
          _handle(ends: ends, start: true),
          _handle(ends: ends, start: false),
        ],
        _copyButton(anchor, viewHeight),
      ],
    );
  }

  Widget _copyButton(SelectionAnchor anchor, double viewHeight) {
    final place = copyChipPlace(
      topRow: anchor.topRow,
      leftCol: anchor.leftCol,
      rightCol: anchor.rightCol,
      cellWidth: widget.cellWidth,
      cellHeight: widget.cellHeight,
      padLeft: widget.padLeft,
      padTop: widget.padTop,
      lift: widget.lift,
      pinch: widget.pinch,
      panX: widget.panX,
      panY: widget.panY,
      cropX: widget.cropX,
      viewWidth: widget.width,
      viewHeight: viewHeight,
      chipWidth: 132,
      chipHeight: 34,
    );
    return Positioned(
      top: place.top,
      left: place.centerX,
      child: FractionalTranslation(
        translation: const Offset(-0.5, 0),
        child: _CopyChip(
          onCopy: () =>
              unawaited(copyTerminalSelection(context, widget.controller)),
          onSelectAll: widget.controller.selectParagraph,
        ),
      ),
    );
  }

  Widget _handle({required SelectionEnds ends, required bool start}) {
    final caret = selectionCaret(
      row: start ? ends.startRow : ends.endRow,
      col: start ? ends.startCol : ends.endCol,
      after: !start,
      cellWidth: widget.cellWidth,
      cellHeight: widget.cellHeight,
      padLeft: widget.padLeft,
      padTop: widget.padTop,
      lift: widget.lift,
      pinch: widget.pinch,
      panX: widget.panX,
      panY: widget.panY,
      cropX: widget.cropX,
    );
    const hit = 44.0;
    final stem = widget.cellHeight * widget.pinch;
    return Positioned(
      left: caret.dx - hit / 2,
      top: caret.dy,
      width: hit,
      height: stem + 22,
      child: Listener(
        key: Key(start ? 'selection-handle-start' : 'selection-handle-end'),
        behavior: HitTestBehavior.opaque,
        onPointerDown: (event) {
          _beginHandle(
            start ? ends.screenEnd : ends.screenStart,
            start ? ends.startRow : ends.endRow,
            start ? ends.startCol : ends.endCol,
          );
          _dragHandle(event.position);
        },
        onPointerMove: (event) => _dragHandle(event.position),
        onPointerUp: (_) => _endHandle(),
        onPointerCancel: (_) => _endHandle(),
        child: _HandleKnob(stem: stem),
      ),
    );
  }
}

class _CopyChip extends StatelessWidget {
  const _CopyChip({required this.onCopy, required this.onSelectAll});

  final VoidCallback onCopy;
  final VoidCallback onSelectAll;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final style = TextStyle(
      color: c.accent,
      fontSize: Fs.small,
      fontWeight: Fw.medium,
    );
    return Material(
      color: c.raised,
      borderRadius: BorderRadius.circular(Rd.button),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          InkWell(
            onTap: onCopy,
            borderRadius: const BorderRadius.horizontal(
              left: Radius.circular(Rd.button),
            ),
            child: Padding(
              padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 8),
              child: Text(context.l10n.commonCopy, style: style),
            ),
          ),
          Container(width: 1, height: 16, color: c.line),
          InkWell(
            onTap: onSelectAll,
            borderRadius: const BorderRadius.horizontal(
              right: Radius.circular(Rd.button),
            ),
            child: Padding(
              padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 8),
              child: Text(context.l10n.commonSelectAll, style: style),
            ),
          ),
        ],
      ),
    );
  }
}

final _lineBreak = RegExp(r'\r\n?|\n');
final _whitespace = RegExp(r'\s');
final _programWithArgument = RegExp(r'^[\w./~+-]+[ \t]+[\w./~+-]');
final _commandOption = RegExp(r'(?:^|[ \t])--?[A-Za-z][\w-]*(?:[ \t=]|$)');
final _argumentStart = RegExp(r'^(?:--?[A-Za-z]|/|\.{1,2}/|~/)');
final _explicitContinuation = RegExp(r'(?:\\|[:;]|\|\|?|&&)$');

Future<void> copyTerminalSelection(
  BuildContext context,
  TerminalController controller,
) async {
  // 终端的软换行已由选区格式化器合并，留下的换行可能来自 CLI 排版或原文。
  final original = trimPastedNewlines(controller.selectedText());
  final lines = original.split(_lineBreak);
  final text = lines.length > 1
      ? await showPosseDialog<String>(
          context: context,
          builder: (_) => _SelectionCopyDialog(original, lines),
        )
      : original;
  if (text == null || !context.mounted) return;
  controller.clearSelection();
  if (text.isNotEmpty) await Clipboard.setData(ClipboardData(text: text));
}

enum _CopyMode { original, spaced, joined }

bool _isWebLink(String value) {
  final uri = Uri.tryParse(value);
  return uri != null &&
      (uri.scheme == 'http' || uri.scheme == 'https') &&
      uri.host.isNotEmpty;
}

// 选中建议仅改变预览，终端画面本身无法区分程序折行和手写的换行。
bool _looksLikeWrappedCommand(List<String> lines, List<String> fragments) {
  if (lines.length != fragments.length ||
      !_programWithArgument.hasMatch(fragments.first) ||
      !fragments.any(_commandOption.hasMatch) ||
      fragments
          .take(fragments.length - 1)
          .any(_explicitContinuation.hasMatch)) {
    return false;
  }
  final indent = lines.first.length - lines.first.trimLeft().length;
  return lines
          .skip(1)
          .every((line) => line.length - line.trimLeft().length > indent) ||
      fragments.skip(1).every(_argumentStart.hasMatch);
}

class _SelectionCopyDialog extends StatefulWidget {
  const _SelectionCopyDialog(this.original, this.lines);

  final String original;
  final List<String> lines;

  @override
  State<_SelectionCopyDialog> createState() => _SelectionCopyDialogState();
}

class _SelectionCopyDialogState extends State<_SelectionCopyDialog> {
  late final _fragments = widget.lines
      .map((line) => line.trim())
      .where((line) => line.isNotEmpty)
      .toList();
  late final _contiguousToken =
      _fragments.length == widget.lines.length &&
      !_fragments.any(_whitespace.hasMatch);
  late final _wrappedLink = _contiguousToken && _isWebLink(_fragments.join());
  // 无分隔符只适合选中的同一个词；命令的参数边界需要保留。
  late final _canJoinDirectly =
      _contiguousToken &&
      (_wrappedLink || !_fragments.any(_commandOption.hasMatch));
  late var _mode = _wrappedLink
      ? _CopyMode.joined
      : _looksLikeWrappedCommand(widget.lines, _fragments)
      ? _CopyMode.spaced
      : _CopyMode.original;

  @override
  Widget build(BuildContext context) {
    final l = context.l10n;
    final preview = switch (_mode) {
      .original => widget.original,
      .spaced => _fragments.join(' '),
      .joined => _fragments.join(),
    };
    return AlertDialog(
      title: Text(l.terminalCopySelection),
      scrollable: true,
      content: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Wrap(
            spacing: 8,
            runSpacing: 4,
            children: [
              for (final mode in _CopyMode.values)
                if (mode != .joined || _canJoinDirectly)
                  ChoiceChip(
                    label: Text(switch (mode) {
                      .original => l.terminalCopyKeepLineBreaks,
                      .spaced => l.terminalCopyJoinWithSpace,
                      .joined => l.terminalCopyJoinDirectly,
                    }),
                    selected: _mode == mode,
                    onSelected: (_) => setState(() => _mode = mode),
                  ),
            ],
          ),
          const SizedBox(height: 12),
          SingleChildScrollView(
            scrollDirection: Axis.horizontal,
            child: Text(
              preview,
              softWrap: false,
              style: mono(Fs.small, height: Lh.code),
            ),
          ),
        ],
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(),
          child: Text(l.commonCancel),
        ),
        FilledButton(
          onPressed: () => Navigator.of(context).pop(preview),
          child: Text(l.commonCopy),
        ),
      ],
    );
  }
}

/// 选区一端的竖线和圆点，圆点在格子下面，方便手指拖。
class _HandleKnob extends StatelessWidget {
  const _HandleKnob({required this.stem});

  final double stem;

  @override
  Widget build(BuildContext context) {
    final color = PosseColors.of(context).accent;
    return Column(
      children: [
        Center(
          child: Container(width: 2, height: stem, color: color),
        ),
        Container(
          width: 14,
          height: 14,
          decoration: BoxDecoration(color: color, shape: BoxShape.circle),
        ),
      ],
    );
  }
}
