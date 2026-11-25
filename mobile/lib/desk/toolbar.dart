import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../chat/latency_label.dart';
import '../l10n/l10n.dart';
import '../ui/theme.dart';
import 'chrome.dart' show deskBarTrack, deskFpsLabel, deskFpsValue;

const deskControlExtent = 40.0;

typedef DeskSlotDefaultPosition = Offset Function(Size size, Size childSize);

/// anchor 按未被键盘遮住前的范围保存（null 时用 [defaultPosition]），键盘只限制实际显示位置。
class DeskBarSlot extends StatefulWidget {
  const DeskBarSlot({
    super.key,
    required this.anchor,
    required this.onDragEnd,
    this.defaultPosition,
    this.bottomInset = 0,
    this.toggleAlignment,
    required this.child,
  });

  final ValueNotifier<Offset?> anchor;
  final VoidCallback onDragEnd;
  final DeskSlotDefaultPosition? defaultPosition;
  final double bottomInset;
  final Alignment? toggleAlignment;
  final Widget child;

  @override
  State<DeskBarSlot> createState() => _DeskBarSlotState();
}

class _DeskBarSlotState extends State<DeskBarSlot> {
  final _slot = GlobalKey();
  final _bar = GlobalKey();
  Offset? _dragAt;

  void _finishDrag() {
    if (_dragAt == null) return;
    _dragAt = null;
    widget.onDragEnd();
  }

  @override
  void dispose() {
    if (_dragAt != null) {
      _dragAt = null;
      final finish = widget.onDragEnd;
      scheduleMicrotask(finish);
    }
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return ValueListenableBuilder<Offset?>(
      valueListenable: widget.anchor,
      builder: (context, anchor, child) => CustomSingleChildLayout(
        key: _slot,
        delegate: _BarLayout(
          anchor,
          widget.defaultPosition,
          widget.bottomInset,
          widget.toggleAlignment,
        ),
        child: child,
      ),
      child: GestureDetector(
        key: _bar,
        behavior: HitTestBehavior.opaque,
        onPanStart: (_) {
          final slot = _slot.currentContext!.findRenderObject()! as RenderBox;
          final bar = _bar.currentContext!.findRenderObject()! as RenderBox;
          _dragAt = bar.localToGlobal(Offset.zero, ancestor: slot);
        },
        onPanUpdate: (details) {
          final slot = _slot.currentContext!.findRenderObject()! as RenderBox;
          final bar = _bar.currentContext!.findRenderObject()! as RenderBox;
          // 同一帧内也会收到多次移动，不能用尚未重排的 RenderBox 位置累加。
          final from = _dragAt!;
          final layout = _BarLayout(
            widget.anchor.value,
            widget.defaultPosition,
            widget.bottomInset,
            widget.toggleAlignment,
          );
          final at = layout.constrainPosition(
            slot.size,
            bar.size,
            from + details.delta,
          );
          _dragAt = at;
          widget.anchor.value = layout.anchorAfterDrag(
            slot.size,
            bar.size,
            from,
            at,
          );
        },
        onPanEnd: (_) => _finishDrag(),
        onPanCancel: _finishDrag,
        // 长按提示会赢得手势竞争，让按住后再拖失效；仍保留悬停提示和无障碍名称。
        child: TooltipTheme(
          data: TooltipTheme.of(context)
              .copyWith(triggerMode: TooltipTriggerMode.manual),
          child: widget.child,
        ),
      ),
    );
  }
}

class _BarLayout extends SingleChildLayoutDelegate {
  _BarLayout(
    this.anchor,
    this.defaultPosition,
    this.bottomInset,
    this.toggleAlignment,
  );

  final Offset? anchor;
  final DeskSlotDefaultPosition? defaultPosition;
  final double bottomInset;
  final Alignment? toggleAlignment;

  Size _anchorSize(Size bar) =>
      toggleAlignment == null ? bar : const Size.square(deskControlExtent);

  Offset _toggleOffset(Size bar) =>
      toggleAlignment?.alongOffset(
        Offset(bar.width - deskControlExtent, bar.height - deskControlExtent),
      ) ??
      Offset.zero;

  Offset constrainPosition(Size size, Size bar, Offset position) => Offset(
    position.dx.clamp(0.0, math.max(0.0, size.width - bar.width)),
    position.dy.clamp(
      0.0,
      math.max(0.0, size.height - bar.height - bottomInset),
    ),
  );

  Offset anchorAfterDrag(Size size, Size bar, Offset from, Offset at) {
    final reference = _anchorSize(bar);
    final toggle = at + _toggleOffset(bar);
    final current = anchor;
    return Offset(
      current != null && from.dx == at.dx
          ? current.dx
          : (toggle.dx / deskBarTrack(size.width, reference.width)).clamp(0, 1),
      current != null && from.dy == at.dy
          ? current.dy
          : (toggle.dy / deskBarTrack(size.height, reference.height)).clamp(
              0,
              1,
            ),
    );
  }

  @override
  BoxConstraints getConstraintsForChild(BoxConstraints constraints) =>
      BoxConstraints.loose(
        Size(
          constraints.maxWidth,
          math.max(0, constraints.maxHeight - bottomInset),
        ),
      );

  @override
  Offset getPositionForChild(Size size, Size childSize) {
    final current = anchor;
    if (current == null) {
      final raw = defaultPosition?.call(size, childSize) ?? Offset.zero;
      return constrainPosition(size, childSize, raw);
    }
    final reference = _anchorSize(childSize);
    final x = math.max(0.0, size.width - reference.width);
    final y = math.max(0.0, size.height - reference.height);
    // 以收起按钮为锚点，有足够空间时展开和收起都留在手指下。
    return constrainPosition(
      size,
      childSize,
      Offset(current.dx * x, current.dy * y) - _toggleOffset(childSize),
    );
  }

  @override
  bool shouldRelayout(_BarLayout oldDelegate) =>
      anchor != oldDelegate.anchor ||
      defaultPosition != oldDelegate.defaultPosition ||
      bottomInset != oldDelegate.bottomInset ||
      toggleAlignment != oldDelegate.toggleAlignment;
}

/// 一个可拖动浮动控件的规格，供 [DeskFloatingLayer] 统一排布并避免互相遮挡。
class DeskFloatingItem {
  const DeskFloatingItem({
    required this.id,
    required this.anchor,
    required this.onDragEnd,
    required this.defaultPosition,
    required this.child,
    this.toggleAlignment,
  });

  final Object id;
  final ValueNotifier<Offset?> anchor;
  final VoidCallback onDragEnd;
  final DeskSlotDefaultPosition defaultPosition;
  final Alignment? toggleAlignment;
  final Widget child;
}

/// 共同排布多个可拖动浮动控件：在拖放、重开、键盘挤压或状态胶囊变宽时自动避开重叠，
/// 确保每个控件都保持可点击。空白区域不拦截手势，触摸照常透传给下层桌面画面。
class DeskFloatingLayer extends StatefulWidget {
  const DeskFloatingLayer({
    super.key,
    required this.items,
    this.bottomInset = 0,
  });

  final List<DeskFloatingItem> items;
  final double bottomInset;

  @override
  State<DeskFloatingLayer> createState() => _DeskFloatingLayerState();
}

class _DeskFloatingLayerState extends State<DeskFloatingLayer> {
  final _layerKey = GlobalKey();
  final _itemKeys = <Object, GlobalKey>{};
  final _dragAt = <Object, Offset>{};
  final _dragEnd = <Object, VoidCallback>{};

  GlobalKey _keyOf(Object id) => _itemKeys.putIfAbsent(id, GlobalKey.new);

  void _finishDrag(Object id) {
    final end = _dragEnd.remove(id);
    if (_dragAt.remove(id) == null) return;
    end?.call();
  }

  @override
  void dispose() {
    if (_dragAt.isNotEmpty) {
      final pending = List<VoidCallback>.from(_dragEnd.values);
      _dragAt.clear();
      _dragEnd.clear();
      for (final finish in pending) {
        scheduleMicrotask(finish);
      }
    }
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final listenable = Listenable.merge([
      for (final item in widget.items) item.anchor,
    ]);
    return ListenableBuilder(
      listenable: listenable,
      builder: (context, _) => CustomMultiChildLayout(
        key: _layerKey,
        delegate: _FloatingLayout(widget.items, widget.bottomInset),
        children: [
          for (final item in widget.items)
            LayoutId(
              id: item.id,
              child: GestureDetector(
                key: _keyOf(item.id),
                behavior: HitTestBehavior.opaque,
                onPanStart: (_) {
                  final layer =
                      _layerKey.currentContext!.findRenderObject()!
                          as RenderBox;
                  final box =
                      _keyOf(item.id).currentContext!.findRenderObject()!
                          as RenderBox;
                  _dragAt[item.id] = box.localToGlobal(
                    Offset.zero,
                    ancestor: layer,
                  );
                  _dragEnd[item.id] = item.onDragEnd;
                },
                onPanUpdate: (details) {
                  final layer =
                      _layerKey.currentContext!.findRenderObject()!
                          as RenderBox;
                  final box =
                      _keyOf(item.id).currentContext!.findRenderObject()!
                          as RenderBox;
                  final from = _dragAt[item.id]!;
                  final layout = _BarLayout(
                    item.anchor.value,
                    item.defaultPosition,
                    widget.bottomInset,
                    item.toggleAlignment,
                  );
                  final at = layout.constrainPosition(
                    layer.size,
                    box.size,
                    from + details.delta,
                  );
                  _dragAt[item.id] = at;
                  item.anchor.value = layout.anchorAfterDrag(
                    layer.size,
                    box.size,
                    from,
                    at,
                  );
                },
                onPanEnd: (_) => _finishDrag(item.id),
                onPanCancel: () => _finishDrag(item.id),
                child: TooltipTheme(
                  data: TooltipTheme.of(context)
                      .copyWith(triggerMode: TooltipTriggerMode.manual),
                  child: item.child,
                ),
              ),
            ),
        ],
      ),
    );
  }
}

class _FloatingLayout extends MultiChildLayoutDelegate {
  _FloatingLayout(this.items, this.bottomInset);

  final List<DeskFloatingItem> items;
  final double bottomInset;

  @override
  void performLayout(Size size) {
    final childConstraints = BoxConstraints.loose(
      Size(size.width, math.max(0, size.height - bottomInset)),
    );
    final placed = <Rect>[];
    for (final item in items) {
      final childSize = layoutChild(item.id, childConstraints);
      final single = _BarLayout(
        item.anchor.value,
        item.defaultPosition,
        bottomInset,
        item.toggleAlignment,
      );
      final desired = single.getPositionForChild(size, childSize);
      final pos = _resolveNonOverlapping(
        size: size,
        childSize: childSize,
        desired: desired,
        placed: placed,
        constrain: (p) => single.constrainPosition(size, childSize, p),
      );
      positionChild(item.id, pos);
      placed.add(pos & childSize);
    }
  }

  static Offset _resolveNonOverlapping({
    required Size size,
    required Size childSize,
    required Offset desired,
    required List<Rect> placed,
    required Offset Function(Offset) constrain,
  }) {
    bool free(Offset p) {
      final rect = p & childSize;
      for (final other in placed) {
        if (rect.overlaps(other)) return false;
      }
      return true;
    }

    if (free(desired)) return desired;
    final candidates = <Offset>[];
    void addAround(Rect r, Offset ref) {
      candidates.addAll([
        constrain(Offset(r.left - childSize.width, ref.dy)),
        constrain(Offset(r.right, ref.dy)),
        constrain(Offset(ref.dx, r.top - childSize.height)),
        constrain(Offset(ref.dx, r.bottom)),
        constrain(Offset(r.left - childSize.width, r.top)),
        constrain(Offset(r.right, r.top)),
        constrain(Offset(r.left, r.top - childSize.height)),
        constrain(Offset(r.left, r.bottom)),
      ]);
    }

    for (final r in placed) {
      addAround(r, desired);
    }
    // 三个控件挤在同一角时，从第一轮贴边位置再外扩一步，确保都能错开。
    final firstWave = List<Offset>.from(candidates);
    for (final c in firstWave) {
      for (final r in placed) {
        addAround(r, c);
      }
    }
    Offset? best;
    var bestDist = double.infinity;
    for (final c in candidates) {
      if (!free(c)) continue;
      final d = (c - desired).distanceSquared;
      if (d < bestDist) {
        bestDist = d;
        best = c;
      }
    }
    return best ?? desired;
  }

  @override
  bool shouldRelayout(_FloatingLayout oldDelegate) => true;
}

class DeskBackButton extends StatelessWidget {
  const DeskBackButton({super.key, required this.onBack});

  final VoidCallback onBack;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Material(
      color: c.glass,
      borderRadius: BorderRadius.circular(Rd.pill),
      clipBehavior: Clip.antiAlias,
      child: _DeskButton(
        buttonKey: const Key('desk-toolbar-back'),
        icon: Icons.arrow_back_ios_new_rounded,
        tooltip: context.l10n.deskBack,
        onTap: onBack,
      ),
    );
  }
}

class DeskStatusPill extends StatelessWidget {
  const DeskStatusPill({
    super.key,
    required this.latency,
    required this.unresponsive,
    required this.reconnecting,
    required this.detailsOpen,
    required this.onTap,
    this.fps = -1,
    this.video = false,
    this.still = false,
  });

  final Duration? latency;
  final bool unresponsive;
  final bool reconnecting;
  final bool detailsOpen;
  final VoidCallback onTap;
  final double fps;
  final bool video;
  final bool still;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final unknown = latency == null && !unresponsive;
    final label = reconnecting
        ? l.deskReconnecting
        : unresponsive
        ? l.chatLatencyUnresponsive
        : latencyIsHigh(latency)
        ? l.deskHighLatency
        : null;
    final icon = reconnecting
        ? latencyIcon(null, unresponsive: true)
        : latencyIcon(latency, unresponsive: unresponsive);
    final color = reconnecting
        ? c.honey
        : unknown
        ? c.faint
        : latencyColor(c, latency, unresponsive: unresponsive);
    final fpsText = deskFpsValue(l, fps, video: video, still: still);
    final fpsSemantic = deskFpsLabel(l, fps, video: video, still: still);
    return Tooltip(
      message: l.deskConnectionDetails,
      child: Semantics(
        button: true,
        expanded: detailsOpen,
        value: fpsSemantic,
        child: Material(
          color: detailsOpen ? c.tint : c.glass,
          borderRadius: BorderRadius.circular(Rd.pill),
          clipBehavior: Clip.antiAlias,
          child: InkWell(
            key: const Key('desk-status'),
            onTap: onTap,
            child: ConstrainedBox(
              constraints: BoxConstraints(
                maxWidth: MediaQuery.sizeOf(context).width / 2,
              ),
              child: Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  SizedBox.square(
                    dimension: deskControlExtent,
                    child: Padding(
                      padding: const EdgeInsets.all(2),
                      child: FittedBox(
                        fit: BoxFit.scaleDown,
                        child: Column(
                          mainAxisSize: MainAxisSize.min,
                          children: [
                            Icon(icon, size: Ic.inline, color: color),
                            const SizedBox(height: 2),
                            Text(
                              fpsText,
                              maxLines: 1,
                              softWrap: false,
                              style: TextStyle(
                                fontSize: Fs.tiny,
                                fontWeight: Fw.medium,
                                color: color,
                                fontFeatures: const [
                                  FontFeature.tabularFigures(),
                                ],
                              ),
                            ),
                          ],
                        ),
                      ),
                    ),
                  ),
                  if (label != null) ...[
                    Flexible(
                      child: Text(
                        label,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: statusLineStyle(c.text),
                      ),
                    ),
                    const SizedBox(width: 12),
                  ],
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}

class DeskToolbar extends StatelessWidget {
  const DeskToolbar({
    super.key,
    required this.landscape,
    this.compact = false,
    required this.expanded,
    this.toggleAlignment = Alignment.topLeft,
    required this.keyboardUp,
    required this.shortcuts,
    required this.onToggle,
    required this.onOrientation,
    required this.onKeyboard,
    required this.onShortcuts,
    required this.onSettings,
    this.onView,
  });

  final bool landscape;

  /// 键盘上方放不下一行时临时放进顶部两端控件之间，横向滚动访问所有操作。
  final bool compact;
  final bool expanded;
  final Alignment toggleAlignment;
  final bool keyboardUp;
  final bool shortcuts;
  final VoidCallback onToggle;
  final VoidCallback onOrientation;
  final VoidCallback onKeyboard;
  final VoidCallback onShortcuts;
  final VoidCallback onSettings;

  /// 有屏幕可选或画面已放大时才显示入口。
  final VoidCallback? onView;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final toggle = Semantics(
      button: true,
      expanded: expanded,
      child: _DeskButton(
        buttonKey: const Key('desk-toolbar-toggle'),
        icon: expanded ? Icons.close_rounded : Icons.more_horiz_rounded,
        tooltip: expanded ? l.deskToolbarCollapse : l.deskToolbarExpand,
        onTap: onToggle,
      ),
    );
    if (!expanded) {
      return Material(
        key: const Key('desk-toolbar'),
        color: c.glass,
        borderRadius: BorderRadius.circular(Rd.pill),
        clipBehavior: Clip.antiAlias,
        child: toggle,
      );
    }
    final icons = <Widget>[
      _DeskButton(
        buttonKey: const Key('desk-toolbar-keyboard'),
        icon: keyboardUp ? Icons.keyboard_hide_rounded : Icons.keyboard_rounded,
        tooltip: l.deskKeyboard,
        on: keyboardUp,
        onTap: onKeyboard,
      ),
      _DeskButton(
        buttonKey: const Key('desk-toolbar-shortcuts'),
        icon: Icons.keyboard_command_key_rounded,
        tooltip: l.deskShortcuts,
        on: shortcuts,
        onTap: onShortcuts,
      ),
      if (onView case final pick?)
        _DeskButton(
          buttonKey: const Key('desk-toolbar-view'),
          icon: Icons.desktop_windows_rounded,
          tooltip: l.deskViewTip,
          onTap: pick,
        ),
      _DeskButton(
        buttonKey: const Key('desk-toolbar-orientation'),
        icon: landscape
            ? Icons.stay_current_landscape_rounded
            : Icons.stay_current_portrait_rounded,
        tooltip: landscape ? l.deskToPortrait : l.deskToLandscape,
        onTap: onOrientation,
      ),
      _DeskButton(
        buttonKey: const Key('desk-toolbar-settings'),
        icon: Icons.settings_rounded,
        tooltip: l.settingsTitle,
        onTap: onSettings,
      ),
    ];
    return LayoutBuilder(
      builder: (context, constraints) {
        final extent = (icons.length + 1) * deskControlExtent;
        final horizontal =
            compact || landscape || constraints.maxHeight < extent;
        final toggleAtEnd =
            !compact &&
            (horizontal ? toggleAlignment.x : toggleAlignment.y) > 0;
        final scroll = horizontal && extent > constraints.maxWidth;
        final children = [
          if (!toggleAtEnd) toggle,
          if (scroll)
            Flexible(
              child: SingleChildScrollView(
                scrollDirection: Axis.horizontal,
                child: Row(mainAxisSize: MainAxisSize.min, children: icons),
              ),
            )
          else
            ...icons,
          if (toggleAtEnd) toggle,
        ];
        return Material(
          key: const Key('desk-toolbar'),
          color: c.glass,
          borderRadius: BorderRadius.circular(Rd.card),
          clipBehavior: Clip.antiAlias,
          child: horizontal
              ? Row(mainAxisSize: MainAxisSize.min, children: children)
              : Column(mainAxisSize: MainAxisSize.min, children: children),
        );
      },
    );
  }
}

class _DeskButton extends StatelessWidget {
  const _DeskButton({
    required this.buttonKey,
    required this.icon,
    required this.tooltip,
    required this.onTap,
    this.on = false,
  });

  final Key buttonKey;
  final IconData icon;
  final String tooltip;
  final VoidCallback onTap;
  final bool on;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Tooltip(
      message: tooltip,
      child: Material(
        color: on ? c.tint : Colors.transparent,
        borderRadius: BorderRadius.circular(Rd.button),
        child: InkWell(
          key: buttonKey,
          onTap: onTap,
          borderRadius: BorderRadius.circular(Rd.button),
          child: SizedBox(
            width: deskControlExtent,
            height: deskControlExtent,
            child: Icon(icon, size: Ic.button, color: on ? c.accent : c.text),
          ),
        ),
      ),
    );
  }
}
