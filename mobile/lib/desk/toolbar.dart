import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../chat/latency_label.dart';
import '../l10n/l10n.dart';
import '../ui/theme.dart';
import 'chrome.dart';

const deskControlExtent = 40.0;
const _handleExtent = 24.0;
const _barPadding = 4.0;

/// anchor 按未被键盘遮住前的范围保存，键盘只限制实际显示位置。
class DeskBarSlot extends StatefulWidget {
  const DeskBarSlot({
    super.key,
    required this.anchor,
    required this.onDragEnd,
    this.bottomInset = 0,
    required this.child,
  });

  final ValueNotifier<Offset> anchor;
  final VoidCallback onDragEnd;
  final double bottomInset;
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
        delegate: _BarLayout(anchor, widget.bottomInset),
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
          final next = _dragAt! + details.delta;
          final x = deskBarTrack(slot.size.width, bar.size.width);
          final y = deskBarTrack(slot.size.height, bar.size.height);
          final bottom = math.max(
            0.0,
            slot.size.height - widget.bottomInset - bar.size.height,
          );
          final canMoveX = slot.size.width > bar.size.width;
          final at = Offset(
            next.dx.clamp(0.0, canMoveX ? x : 0.0),
            next.dy.clamp(0.0, bottom),
          );
          _dragAt = at;
          final previous = widget.anchor.value;
          widget.anchor.value = Offset(
            canMoveX ? at.dx / x : previous.dx,
            bottom > 0 ? at.dy / y : previous.dy,
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
  _BarLayout(this.anchor, this.bottomInset);

  final Offset anchor;
  final double bottomInset;

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
    final x = math.max(0.0, size.width - childSize.width);
    final y = math.max(0.0, size.height - childSize.height);
    return Offset(
      anchor.dx * x,
      (anchor.dy * y).clamp(0.0, math.max(0.0, y - bottomInset)),
    );
  }

  @override
  bool shouldRelayout(_BarLayout oldDelegate) =>
      anchor != oldDelegate.anchor || bottomInset != oldDelegate.bottomInset;
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
    required this.fps,
    required this.video,
    this.still = false,
  });

  final Duration? latency;
  final bool unresponsive;
  final double fps;
  final bool video;
  final bool still;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final unknown = latency == null && !unresponsive;
    return Listener(
      behavior: HitTestBehavior.opaque,
      child: Container(
        key: const Key('desk-status'),
        height: deskControlExtent,
        padding: const EdgeInsets.symmetric(horizontal: 12),
        decoration: BoxDecoration(
          color: c.glass,
          borderRadius: BorderRadius.circular(Rd.pill),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(
              unknown
                  ? Icons.signal_cellular_alt_rounded
                  : latencyIcon(latency, unresponsive: unresponsive),
              size: Ic.inline,
              color: unknown
                  ? c.faint
                  : latencyColor(c, latency, unresponsive: unresponsive),
            ),
            const SizedBox(width: 6),
            Text(
              deskFpsLabel(context.l10n, fps, video: video, still: still),
              key: const Key('desk-fps'),
              style: statusLineStyle(c.text),
            ),
          ],
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
    required this.keyboardUp,
    required this.shortcuts,
    required this.debugOpen,
    required this.stopped,
    required this.onOrientation,
    required this.onKeyboard,
    required this.onShortcuts,
    required this.onDebug,
    required this.onSettings,
    required this.onDisconnect,
    this.onView,
  });

  final bool landscape;

  /// 键盘上方放不下一行时临时放进顶部两端控件之间，横向滚动访问所有操作。
  final bool compact;
  final bool keyboardUp;
  final bool shortcuts;
  final bool debugOpen;
  final bool stopped;
  final VoidCallback onOrientation;
  final VoidCallback onKeyboard;
  final VoidCallback onShortcuts;
  final VoidCallback onDebug;
  final VoidCallback onSettings;
  final VoidCallback onDisconnect;

  /// 电脑还没报屏（或太旧不报）时为空，按钮不出现。
  final VoidCallback? onView;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final icons = <Widget>[
      _DeskButton(
        buttonKey: const Key('desk-toolbar-orientation'),
        icon: landscape
            ? Icons.stay_current_landscape_rounded
            : Icons.stay_current_portrait_rounded,
        tooltip: landscape ? l.deskToPortrait : l.deskToLandscape,
        onTap: onOrientation,
      ),
      if (onView case final pick?)
        _DeskButton(
          buttonKey: const Key('desk-toolbar-view'),
          icon: Icons.desktop_windows_rounded,
          tooltip: l.deskViewTip,
          onTap: pick,
        ),
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
      _DeskButton(
        buttonKey: const Key('desk-toolbar-debug'),
        icon: Icons.speed_rounded,
        tooltip: l.deskDebug,
        on: debugOpen,
        onTap: onDebug,
      ),
      _DeskButton(
        buttonKey: const Key('desk-toolbar-settings'),
        icon: Icons.settings_rounded,
        tooltip: l.settingsTitle,
        onTap: onSettings,
      ),
      _DeskButton(
        buttonKey: const Key('desk-toolbar-disconnect'),
        icon: stopped ? Icons.refresh_rounded : Icons.link_off_rounded,
        tooltip: stopped ? l.deskReconnect : l.deskDisconnect,
        onTap: onDisconnect,
      ),
    ];
    return LayoutBuilder(
      builder: (context, constraints) {
        final verticalHeight =
            icons.length * deskControlExtent + _handleExtent + _barPadding * 2;
        final horizontal =
            compact || landscape || constraints.maxHeight < verticalHeight;
        final handle = SizedBox(
          key: const Key('desk-toolbar-handle'),
          width: horizontal ? _handleExtent : deskControlExtent,
          height: horizontal ? deskControlExtent : _handleExtent,
          child: Icon(
            horizontal
                ? Icons.drag_indicator_rounded
                : Icons.drag_handle_rounded,
            size: Ic.small,
            color: c.muted,
          ),
        );
        final children = [if (!compact) handle, ...icons];
        final contentWidth =
            icons.length * deskControlExtent + (compact ? 0 : _handleExtent);
        final availableWidth = math.max(
          0.0,
          constraints.maxWidth - _barPadding * 2,
        );
        final scroll = horizontal && contentWidth > availableWidth;
        return Material(
          key: const Key('desk-toolbar'),
          color: c.glass,
          borderRadius: BorderRadius.circular(Rd.card),
          clipBehavior: Clip.antiAlias,
          child: Padding(
            padding: EdgeInsets.symmetric(
              horizontal: _barPadding,
              vertical: compact ? 0 : _barPadding,
            ),
            child: !horizontal
                ? Column(mainAxisSize: MainAxisSize.min, children: children)
                : scroll
                ? SizedBox(
                    width: availableWidth,
                    child: Row(
                      children: [
                        if (!compact) handle,
                        Expanded(
                          child: SingleChildScrollView(
                            scrollDirection: Axis.horizontal,
                            child: Row(
                              mainAxisSize: MainAxisSize.min,
                              children: icons,
                            ),
                          ),
                        ),
                      ],
                    ),
                  )
                : Row(mainAxisSize: MainAxisSize.min, children: children),
          ),
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
