import 'dart:async';

import 'package:flutter/gestures.dart';
import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';
import 'package:libghostty/libghostty.dart'
    show MouseAction, MouseTracking, Position;
import 'package:meta/meta.dart';

import '../foundation.dart';
import '../links/link_settings.dart';
import 'link_interaction.dart';
import 'terminal_raw_gesture_detector.dart';
import 'terminal_view_binding.dart';

/// Interprets gestures as terminal actions: selection, mouse tracking
/// reports, and focus requests.
///
/// Reports all gestures to [TerminalViewBinding] which handles
/// snapping, scroll offset, and encoding.
@internal
class TerminalGestureDetector extends StatefulWidget {
  final Widget child;
  final int visibleRows;
  final CellMetrics metrics;
  final TerminalViewBinding binding;
  final TerminalGestureSettings settings;
  final LinkInteraction links;
  final ValueChanged<ActivatedLink>? onLinkActivate;
  final ScrollController? scrollController;

  /// Hive: 设了就由外层决定点击后键盘怎么办，这里不再聚焦。
  final VoidCallback? onTap;

  const TerminalGestureDetector({
    super.key,
    required this.child,
    this.visibleRows = 0,
    required this.metrics,
    required this.binding,
    required this.links,
    this.onLinkActivate,
    this.scrollController,
    this.onTap,
    this.settings = const TerminalGestureSettings(),
  });

  @override
  State<TerminalGestureDetector> createState() =>
      _TerminalGestureDetectorState();
}

class _TerminalGestureDetectorState extends State<TerminalGestureDetector> {
  _DragState? _drag;
  Position? _pressCell;
  var _linkPressActive = false;
  Timer? _autoScrollTimer;

  /// Hive: 长按不参与竞技场。10px 用屏幕坐标，免得电脑布局缩放后局部坐标把阈值放大。
  static const _longPressSlop = 10.0;
  static const _longPressDelay = Duration(milliseconds: 500);

  /// Hive: 抬手仍算点击的范围和时限，照 Orca 的 TAP_SLOP / TAP_MAX_MS。
  static const _tapSlop = 24.0;
  static const _tapMax = Duration(milliseconds: 700);

  final _pointers = <int>{};
  Timer? _longPressTimer;
  _Finger? _primary;

  /// 第二指落下后，这一次手势不再算点击。
  var _blocked = false;

  /// 长按已经选中词，这次抬起不要再走按下释放。
  var _holdSelect = false;

  TerminalViewBinding get _binding => widget.binding;

  @override
  Widget build(BuildContext context) {
    return Listener(
      behavior: HitTestBehavior.opaque,
      onPointerDown: _handlePointerDown,
      onPointerMove: _handlePointerMove,
      onPointerUp: _handlePointerUp,
      onPointerCancel: _handlePointerCancel,
      child: TerminalRawGestureDetector(
        onTapDown: _handleTapDown,
        onTapUp: _handleTapUp,
        onTapCancel: _handleTapCancel,
        onDragStart: _handleDragStart,
        onDragUpdate: _handleDragUpdate,
        onDragEnd: _handleDragEnd,
        child: widget.child,
      ),
    );
  }

  @override
  void didUpdateWidget(TerminalGestureDetector oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (widget.metrics != oldWidget.metrics ||
        widget.binding != oldWidget.binding) {
      _binding.clearSelection();
      _stopAutoScroll();
      _drag = null;
      _pressCell = null;
      _cancelLinkPress();
      _cancelLongPress();
      _unpinScroll();
      _pointers.clear();
      _primary = null;
      _blocked = false;
      _holdSelect = false;
    }
  }

  @override
  void dispose() {
    _autoScrollTimer?.cancel();
    _cancelLongPress();
    _unpinScroll();
    super.dispose();
  }

  void _autoScrollTick(Timer timer) {
    final scrollController = widget.scrollController;
    if (scrollController == null || !scrollController.hasClients) return;

    final drag = _drag;
    if (drag == null) {
      _stopAutoScroll();
      return;
    }

    // Hive: 拖到边缘的自动滚动是选区自己要的，钉住的位置跟着它走。
    _autoScrolling = true;
    try {
      _binding.updateSelectionAutoscroll(
        cell: drag.cell,
        localPosition: drag.localPosition,
        rectangle: drag.lastRectangle,
      );
    } finally {
      _autoScrolling = false;
    }
    if (_pinnedPixels != null) _pinnedPixels = scrollController.position.pixels;
  }

  void _cancelLinkPress() {
    if (!_linkPressActive) return;
    _linkPressActive = false;
    widget.links.cancel();
  }

  void _cancelSelectionPress() {
    if (_pressCell == null) return;
    _binding.cancelSelectionGesture();
    _pressCell = null;
  }

  int _clampInt(int value, int min, int max) {
    if (value < min) return min;
    if (value > max) return max;
    return value;
  }

  void _endDrag() {
    final drag = _drag;
    if (drag != null) {
      _releaseSelectionPress(drag.cell);
    } else {
      _releaseSelectionPress();
    }
    _stopAutoScroll();
    _drag = null;
    _unpinScroll();
    _cancelLinkPress();
  }

  void _handleDragEnd() => _endDrag();

  void _handleDragStart(DragStartDetails details) {
    _binding.requestFocus();
    _cancelLinkPress();
    if (_isMouseTracked(HardwareKeyboard.instance.isShiftPressed)) return;
    if (!widget.settings.dragSelection) {
      _cancelSelectionPress();
      return;
    }

    _startDrag(details.localPosition, beginPress: _pressCell == null);
  }

  void _handleDragUpdate(DragUpdateDetails details) {
    if (_isMouseTracked(HardwareKeyboard.instance.isShiftPressed)) return;
    if (_drag != null) _updateDrag(details.localPosition);
  }

  void _handlePointerDown(PointerDownEvent event) {
    // Hive: 只有真正的鼠标才按按下/移动/抬起发给程序。手指滑动不是拖选。
    if (_isMouseKind(event.kind)) {
      if (_binding.mouseTracking != MouseTracking.none) {
        _handleTrackedDown(event);
      }
      return;
    }
    _pointers.add(event.pointer);
    final current = _primary;
    if (current != null) {
      _blocked = true;
      _cancelLongPress();
      _stopAutoScroll();
      _drag = null;
      _unpinScroll();
      _binding.clearSelection();
      _holdSelect = false;
      current.selectedWord = false;
      current.brokeSelection = true;
      return;
    }
    final finger = _Finger(
      pointer: event.pointer,
      local: event.localPosition,
      global: event.position,
      stamp: event.timeStamp,
      kind: event.kind,
    );
    _primary = finger;
    // Hive: 已有选区时这一指只清掉，滑动照常，不再长按选词。
    if (_binding.terminal.selection != null) {
      _binding.clearSelection();
      finger.suppressSelect = true;
      _unpinScroll();
      return;
    }
    // Hive: 程序在跟踪鼠标时也不挡住长按。点按仍然在抬手后发一键，长按留给复制。
    _armLongPress();
  }

  void _handlePointerMove(PointerMoveEvent event) {
    if (_isMouseKind(event.kind)) {
      if (_binding.mouseTracking != MouseTracking.none) {
        _handleTrackedMove(event);
      }
      return;
    }
    final finger = _primary;
    if (finger == null || event.pointer != finger.pointer) return;
    final delta = event.position - finger.global;
    if (delta.dx.abs() + delta.dy.abs() <= _longPressSlop) return;
    _cancelLongPress();
    if (!finger.selectedWord) return;
    // Hive: 长按已经选中词，这一指接着移动是在扩大选区，不再当成滑动清掉。
    final local = event.localPosition;
    if (_drag == null) {
      _startDrag(local, beginPress: false);
      _pinScroll();
    }
    _updateDrag(local);
  }

  void _handlePointerUp(PointerUpEvent event) {
    if (_isMouseKind(event.kind)) {
      if (_binding.mouseTracking != MouseTracking.none) {
        _handleTrackedUp(event);
      }
      return;
    }
    _pointers.remove(event.pointer);
    final finger = _primary;
    if (finger != null && event.pointer == finger.pointer) {
      _cancelLongPress();
      if (_drag != null) _endDrag();
      _primary = null;
      _holdSelect = false;
      if (_isTap(finger, event)) _confirmTap(finger);
    }
    if (_pointers.isEmpty) _blocked = false;
  }

  void _handlePointerCancel(PointerCancelEvent event) {
    _pointers.remove(event.pointer);
    if (_primary?.pointer == event.pointer) {
      _cancelLongPress();
      if (_drag != null) _endDrag();
      _primary = null;
      _holdSelect = false;
    }
    if (_pointers.isEmpty) _blocked = false;
  }

  bool _isTap(_Finger finger, PointerUpEvent event) {
    if (_blocked || finger.selectedWord || finger.brokeSelection) return false;
    final delta = event.position - finger.global;
    if (delta.dx.abs() + delta.dy.abs() > _tapSlop) return false;
    return event.timeStamp - finger.stamp <= _tapMax;
  }

  /// Hive: 点击成立后才聚焦、开链接，或给正在跟踪鼠标的程序发一次点击。
  void _confirmTap(_Finger finger) {
    final onTap = widget.onTap;
    if (onTap == null) _binding.requestFocus();
    if (widget.links.handlePress(
      localPosition: finger.local,
      metrics: widget.metrics,
      pointerKind: finger.kind,
      virtualMods: _binding.virtualMods,
    )) {
      final link = widget.links.handleRelease(
        localPosition: finger.local,
        metrics: widget.metrics,
      );
      if (link != null) widget.onLinkActivate?.call(link);
      return;
    }
    onTap?.call();
    if (!_isMouseTracked(HardwareKeyboard.instance.isShiftPressed)) return;
    _sendMouseEvent(.press, finger.local);
    _sendMouseEvent(.release, finger.local);
  }

  void _armLongPress() {
    _cancelLongPress();
    final finger = _primary;
    if (finger == null || finger.suppressSelect) return;
    if (!widget.settings.longPressSelection) return;
    _longPressTimer = Timer(_longPressDelay, () {
      _longPressTimer = null;
      final held = _primary;
      if (!mounted || held == null || held.suppressSelect) return;
      _selectWord(held.local);
      held.selectedWord = true;
    });
  }

  void _cancelLongPress() {
    _longPressTimer?.cancel();
    _longPressTimer = null;
  }

  /// Hive: 同一位置立刻再按一次，落在双击时限内，用已有的词选。接着拖是在扩大这块选区。
  void _selectWord(Offset position) {
    _handleSelectionPress(position);
    _handleSelectionPress(position);
    _pressCell = null;
    _holdSelect = true;
  }

  double? _pinnedPixels;
  var _autoScrolling = false;

  /// Hive: 扩大选区时按住滚动位置，免得这一指同时把画面滚走。
  void _pinScroll() {
    final scroll = widget.scrollController;
    if (scroll == null || !scroll.hasClients || _pinnedPixels != null) return;
    _pinnedPixels = scroll.position.pixels;
    scroll.addListener(_restorePinnedScroll);
  }

  void _restorePinnedScroll() {
    final scroll = widget.scrollController;
    final pinned = _pinnedPixels;
    if (scroll == null || pinned == null || !scroll.hasClients) return;
    if (_autoScrolling) return;
    if ((scroll.position.pixels - pinned).abs() < 0.5) return;
    scroll.jumpTo(pinned);
  }

  void _unpinScroll() {
    widget.scrollController?.removeListener(_restorePinnedScroll);
    _pinnedPixels = null;
  }

  void _handleSelectionPress(Offset position) {
    final cell = widget.metrics.cellAt(position);
    _binding.handleSelectionPress(
      cell: cell,
      localPosition: position,
      settings: widget.settings,
    );
    _pressCell = cell;
  }

  void _handleTapDown(TapDownDetails details) {
    // Hive: 不是鼠标时大约 100ms 就会到这里，这时往往还在滑动。不聚焦、不选字。
    if (!_isMouseKind(details.kind)) return;
    _binding.requestFocus();
    if (_isMouseTracked(HardwareKeyboard.instance.isShiftPressed)) return;
    if (widget.links.handlePress(
      localPosition: details.localPosition,
      metrics: widget.metrics,
      pointerKind: details.kind ?? .mouse,
      virtualMods: _binding.virtualMods,
    )) {
      _linkPressActive = true;
      _cancelSelectionPress();
      _cancelLongPress();
      return;
    }
    _handleSelectionPress(details.localPosition);
  }

  void _handleTapCancel() {
    // Hive: 点击识别器输给滚动时也会到这里，手指可能还按着。
    // 长按计时只由这一指自己的移动和抬起取消。
    _holdSelect = false;
    if (_pressCell == null) return;
    _binding.cancelSelectionGesture();
    _pressCell = null;
  }

  void _handleTapUp(TapUpDetails details) {
    if (!_isMouseKind(details.kind)) return;
    if (_linkPressActive) {
      _linkPressActive = false;
      final link = widget.links.handleRelease(
        localPosition: details.localPosition,
        metrics: widget.metrics,
      );
      if (link != null) widget.onLinkActivate?.call(link);
      return;
    }
    if (_holdSelect) {
      _holdSelect = false;
      return;
    }
    if (_pressCell == null) return;
    _releaseSelectionPress(widget.metrics.cellAt(details.localPosition));
  }

  void _handleTrackedDown(PointerDownEvent event) {
    final shift =
        event.buttons & kSecondaryButton != 0 ||
        HardwareKeyboard.instance.isShiftPressed;
    if (!_isMouseTracked(shift)) return;
    _sendMouseEvent(.press, event.localPosition);
  }

  void _handleTrackedMove(PointerMoveEvent event) {
    if (!_isMouseTracked(HardwareKeyboard.instance.isShiftPressed)) return;
    _sendMouseEvent(.motion, event.localPosition);
  }

  void _handleTrackedUp(PointerUpEvent event) {
    if (!_isMouseTracked(HardwareKeyboard.instance.isShiftPressed)) return;
    _sendMouseEvent(.release, event.localPosition);
  }

  bool _isBlockModifierPressed() {
    final modifier = widget.settings.blockSelectionModifier;
    if (modifier == null) return false;
    final keyboard = HardwareKeyboard.instance;
    final mods = _binding.virtualMods;
    return switch (modifier) {
      .alt => keyboard.isAltPressed || mods.hasAlt,
      .meta => keyboard.isMetaPressed || mods.hasSuper,
      .shift => keyboard.isShiftPressed || mods.hasShift,
      .control => keyboard.isControlPressed || mods.hasCtrl,
    };
  }

  bool _isMouseTracked(bool shift) {
    return _binding.mouseTracking != .none &&
        !shift &&
        !_binding.virtualMods.hasShift;
  }

  void _releaseSelectionPress([Position? cell]) {
    cell ??= _pressCell;
    if (cell == null) return;
    _binding.handleSelectionRelease(cell);
    _pressCell = null;
  }

  void _sendMouseEvent(MouseAction action, Offset position) {
    _binding.handleMouseEvent((
      action: action,
      button: .left,
      pixelX: position.dx,
      pixelY: position.dy,
    ));
  }

  void _startAutoScroll() {
    if (_autoScrollTimer != null) return;
    _autoScrollTimer = Timer.periodic(
      const Duration(milliseconds: 50),
      _autoScrollTick,
    );
  }

  void _startDrag(
    Offset position, {
    bool rectangle = false,
    bool beginPress = false,
  }) {
    final cell = widget.metrics.cellAt(position);
    final block = rectangle || _isBlockModifierPressed();
    _drag = _DragState(cell, position, baseRectangle: block);
    if (beginPress) _handleSelectionPress(position);
  }

  void _stopAutoScroll() {
    _autoScrollTimer?.cancel();
    _autoScrollTimer = null;
  }

  void _updateDrag(Offset position) {
    final drag = _drag;
    if (drag == null) return;
    final cell = widget.metrics.cellAt(position);
    drag.cell = cell;
    drag.localPosition = position;

    final visibleRows = widget.visibleRows;
    if (visibleRows > 0) {
      if (cell.row < 0) {
        _startAutoScroll();
      } else if (cell.row >= visibleRows) {
        _startAutoScroll();
      } else {
        _stopAutoScroll();
      }
    }

    final clampedRow = visibleRows > 0
        ? _clampInt(cell.row, 0, visibleRows - 1)
        : cell.row;
    final clampedCell = Position(row: clampedRow, col: cell.col);
    final rectangle = drag.baseRectangle || _isBlockModifierPressed();
    if (clampedCell == drag.lastCell && rectangle == drag.lastRectangle) {
      return;
    }
    drag.lastCell = clampedCell;
    drag.lastRectangle = rectangle;

    _binding.updateSelectionDrag(
      cell: clampedCell,
      localPosition: position,
      rectangle: rectangle,
    );
  }
}

bool _isMouseKind(PointerDeviceKind? kind) => kind == PointerDeviceKind.mouse;

class _Finger {
  _Finger({
    required this.pointer,
    required this.local,
    required this.global,
    required this.stamp,
    required this.kind,
  });

  final int pointer;
  final Offset local;
  final Offset global;
  final Duration stamp;
  final PointerDeviceKind kind;
  var suppressSelect = false;
  var selectedWord = false;
  var brokeSelection = false;
}

class _DragState {
  Position cell;
  Offset localPosition;
  final bool baseRectangle;
  bool lastRectangle;
  Position? lastCell;

  _DragState(this.cell, this.localPosition, {required this.baseRectangle})
    : lastRectangle = baseRectangle;
}
