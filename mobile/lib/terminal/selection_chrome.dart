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
  var _multiline = false;
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
    _multiline = widget.controller.selectionHasLineBreak;
    _anchor = widget.controller.selectionAnchor;
    _ends = widget.controller.selectionEnds;
  }

  void _onController() {
    final anchor = widget.controller.selectionAnchor;
    final ends = widget.controller.selectionEnds;
    final selected = widget.controller.hasSelection;
    final multiline = widget.controller.selectionHasLineBreak;
    // 拖把手回滚时另一端会滚出屏幕，ends 变成 null；留着上一份，被拖的把手才不会被拆掉。
    final kept = ends ?? (_fixedEnd != null ? _ends : null);
    if (!mounted ||
        (selected == _selected &&
            multiline == _multiline &&
            anchor == _anchor &&
            kept == _ends)) {
      return;
    }
    setState(() {
      _selected = selected;
      _multiline = multiline;
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

  Future<void> _copySelection() async {
    // 选到行尾或整段时末尾带着换行，粘贴到哪都会多一个回车
    final text = trimPastedNewlines(widget.controller.selectedText());
    widget.controller.clearSelection();
    if (text.isEmpty) return;
    await Clipboard.setData(ClipboardData(text: text));
  }

  Future<void> _copySingleLine() async {
    final controller = widget.controller;
    final original = trimPastedNewlines(controller.selectedText());
    if (!_lineBreak.hasMatch(original)) return;
    final text = await showPosseDialog<String>(
      context: context,
      builder: (_) => _SingleLineDialog(original),
    );
    if (text == null || !mounted) return;
    if (identical(controller, widget.controller)) controller.clearSelection();
    await Clipboard.setData(ClipboardData(text: text));
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
      chipWidth: _multiline ? 164 : 132,
      chipHeight: 34,
    );
    return Positioned(
      top: place.top,
      left: place.centerX,
      child: FractionalTranslation(
        translation: const Offset(-0.5, 0),
        child: _CopyChip(
          onCopy: () => unawaited(_copySelection()),
          onSelectAll: widget.controller.selectParagraph,
          onCopySingleLine: _multiline
              ? () => unawaited(_copySingleLine())
              : null,
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
  const _CopyChip({
    required this.onCopy,
    required this.onSelectAll,
    this.onCopySingleLine,
  });

  final VoidCallback onCopy;
  final VoidCallback onSelectAll;
  final VoidCallback? onCopySingleLine;

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
            borderRadius: onCopySingleLine == null
                ? const BorderRadius.horizontal(
                    right: Radius.circular(Rd.button),
                  )
                : null,
            child: Padding(
              padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 8),
              child: Text(context.l10n.commonSelectAll, style: style),
            ),
          ),
          if (onCopySingleLine != null) ...[
            Container(width: 1, height: 16, color: c.line),
            InkWell(
              onTap: onCopySingleLine,
              borderRadius: const BorderRadius.horizontal(
                right: Radius.circular(Rd.button),
              ),
              child: Tooltip(
                message: context.l10n.terminalCopySingleLine,
                child: Padding(
                  padding: const EdgeInsets.all(8),
                  child: Icon(
                    Icons.wrap_text_rounded,
                    color: c.accent,
                    size: Ic.small,
                  ),
                ),
              ),
            ),
          ],
        ],
      ),
    );
  }
}

final _lineBreak = RegExp(r'[ \t]*(?:\r\n?|\n)(?:[ \t]*(?:\r\n?|\n))*[ \t]*');

class _SingleLineDialog extends StatefulWidget {
  const _SingleLineDialog(this.original);

  final String original;

  @override
  State<_SingleLineDialog> createState() => _SingleLineDialogState();
}

class _SingleLineDialogState extends State<_SingleLineDialog> {
  var _space = true;

  @override
  Widget build(BuildContext context) {
    final l = context.l10n;
    final preview = widget.original.replaceAll(_lineBreak, _space ? ' ' : '');
    return AlertDialog(
      title: Text(l.terminalCopySingleLine),
      scrollable: true,
      content: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Wrap(
            spacing: 8,
            runSpacing: 4,
            children: [
              ChoiceChip(
                label: Text(l.terminalCopyJoinWithSpace),
                selected: _space,
                onSelected: (_) => setState(() => _space = true),
              ),
              ChoiceChip(
                label: Text(l.terminalCopyJoinDirectly),
                selected: !_space,
                onSelected: (_) => setState(() => _space = false),
              ),
            ],
          ),
          const SizedBox(height: 12),
          Text(preview, style: mono(Fs.small, height: Lh.code)),
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
