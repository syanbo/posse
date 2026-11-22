import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../chat/latency_label.dart';
import '../l10n/l10n.dart';
import '../ui/theme.dart';
import 'chrome.dart' show deskBarTrack;

const deskControlExtent = 40.0;

/// anchor 按未被键盘遮住前的范围保存，键盘只限制实际显示位置。
class DeskBarSlot extends StatefulWidget {
  const DeskBarSlot({
    super.key,
    required this.anchor,
    required this.onDragEnd,
    this.bottomInset = 0,
    this.toggleAlignment,
    required this.child,
  });

  final ValueNotifier<Offset> anchor;
  final VoidCallback onDragEnd;
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
  Widget build(BuildContext context) {
    return ValueListenableBuilder<Offset>(
      valueListenable: widget.anchor,
      builder: (context, anchor, child) => CustomSingleChildLayout(
        key: _slot,
        delegate: _BarLayout(
          anchor,
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
  _BarLayout(this.anchor, this.bottomInset, this.toggleAlignment);

  final Offset anchor;
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
    return Offset(
      from.dx == at.dx
          ? anchor.dx
          : toggle.dx / deskBarTrack(size.width, reference.width),
      from.dy == at.dy
          ? anchor.dy
          : toggle.dy / deskBarTrack(size.height, reference.height),
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
    final reference = _anchorSize(childSize);
    final x = math.max(0.0, size.width - reference.width);
    final y = math.max(0.0, size.height - reference.height);
    // 以收起按钮为锚点，有足够空间时展开和收起都留在手指下。
    return constrainPosition(
      size,
      childSize,
      Offset(anchor.dx * x, anchor.dy * y) - _toggleOffset(childSize),
    );
  }

  @override
  bool shouldRelayout(_BarLayout oldDelegate) =>
      anchor != oldDelegate.anchor ||
      bottomInset != oldDelegate.bottomInset ||
      toggleAlignment != oldDelegate.toggleAlignment;
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
  });

  final Duration? latency;
  final bool unresponsive;
  final bool reconnecting;
  final bool detailsOpen;
  final VoidCallback onTap;

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
    return Tooltip(
      message: l.deskConnectionDetails,
      child: Semantics(
        button: true,
        expanded: detailsOpen,
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
                    child: Icon(icon, size: Ic.medium, color: color),
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
