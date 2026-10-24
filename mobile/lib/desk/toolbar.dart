import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';

import '../chat/latency_label.dart';
import '../ui/theme.dart';
import 'chrome.dart';

const _fade = Duration(milliseconds: 160);

/// 竖屏沿顶边横着放，横屏贴着没有刘海的那一侧竖着放。空的地方不吃点击。
class DeskBarSlot extends StatefulWidget {
  const DeskBarSlot({
    super.key,
    required this.anchor,
    required this.onDrag,
    required this.child,
    this.vertical = false,
    this.onRight = true,
  });

  /// 0 靠起点，1 靠终点，0.5 居中。父组件已经把它限制在安全区里。
  final ValueListenable<double> anchor;
  final ValueChanged<double> onDrag;
  final Widget child;

  /// 横屏沿侧边上下拖。竖屏沿顶边左右拖。
  final bool vertical;

  /// 横屏贴右还是贴左。竖屏不用。
  final bool onRight;

  @override
  State<DeskBarSlot> createState() => _DeskBarSlotState();
}

class _DeskBarSlotState extends State<DeskBarSlot> {
  final _key = GlobalKey();

  /// 拖动当时量。展开动画的第一帧量过就会把轨道算窄，接着拖不跟手。
  double _span(bool vertical) {
    final box = _key.currentContext?.findRenderObject() as RenderBox?;
    if (box == null || !box.hasSize) return 0;
    return vertical ? box.size.height : box.size.width;
  }

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(
      builder: (context, constraints) {
        return ValueListenableBuilder<double>(
          valueListenable: widget.anchor,
          builder: (context, anchor, child) {
            return Align(
              alignment: widget.vertical
                  ? Alignment(widget.onRight ? 1 : -1, deskBarAlignment(anchor))
                  : Alignment(deskBarAlignment(anchor), -1),
              child: child,
            );
          },
          child: GestureDetector(
            key: _key,
            onHorizontalDragUpdate: widget.vertical
                ? null
                : (details) {
                    final track = deskBarTrack(
                      constraints.maxWidth,
                      _span(false),
                    );
                    widget.onDrag(details.delta.dx / track);
                  },
            onVerticalDragUpdate: widget.vertical
                ? (details) {
                    final track = deskBarTrack(
                      constraints.maxHeight,
                      _span(true),
                    );
                    widget.onDrag(details.delta.dy / track);
                  }
                : null,
            child: widget.child,
          ),
        );
      },
    );
  }
}

class DeskToolbar extends StatelessWidget {
  const DeskToolbar({
    super.key,
    required this.expanded,
    required this.latency,
    required this.unresponsive,
    required this.fps,
    required this.video,
    required this.landscape,
    required this.keyboardUp,
    required this.shortcuts,
    required this.zoomed,
    required this.debugOpen,
    required this.stopped,
    required this.onToggle,
    required this.onBack,
    required this.onOrientation,
    required this.onKeyboard,
    required this.onShortcuts,
    required this.onZoom,
    required this.onDebug,
    required this.onDisconnect,
  });

  final bool expanded;
  final Duration? latency;
  final bool unresponsive;
  final double fps;
  final bool video;
  final bool landscape;
  final bool keyboardUp;
  final bool shortcuts;
  final ValueListenable<bool> zoomed;
  final bool debugOpen;
  final bool stopped;
  final VoidCallback onToggle;
  final VoidCallback onBack;
  final VoidCallback onOrientation;
  final VoidCallback onKeyboard;
  final VoidCallback onShortcuts;
  final VoidCallback onZoom;
  final VoidCallback onDebug;
  final VoidCallback onDisconnect;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    return Material(
      key: const Key('desk-toolbar'),
      color: c.card.withValues(alpha: 0.94),
      borderRadius: BorderRadius.circular(999),
      clipBehavior: Clip.antiAlias,
      child: AnimatedSize(
        duration: _fade,
        curve: Curves.easeOutCubic,
        alignment: Alignment.center,
        child: expanded ? _open(c) : _shut(c),
      ),
    );
  }

  Widget _shut(HiveColors c) {
    final signal = _signal(c);
    final icon = Icon(signal.$1, size: 14, color: signal.$2);
    final label = Text(
      deskFpsLabel(fps, video: video),
      key: const Key('desk-fps'),
      style: statusLineStyle(c.text),
    );
    return InkWell(
      onTap: onToggle,
      child: Padding(
        padding: landscape
            ? const EdgeInsets.symmetric(horizontal: 6, vertical: 8)
            : const EdgeInsets.symmetric(horizontal: 10, vertical: 6),
        child: landscape
            ? Column(
                mainAxisSize: MainAxisSize.min,
                children: [icon, const SizedBox(height: 4), label],
              )
            : Row(
                mainAxisSize: MainAxisSize.min,
                children: [icon, const SizedBox(width: 6), label],
              ),
      ),
    );
  }

  Widget _open(HiveColors c) {
    final icons = <Widget>[
      _icon(
        c,
        key: const Key('desk-toolbar-back'),
        icon: Icons.arrow_back_ios_new_rounded,
        tooltip: '返回',
        onTap: onBack,
      ),
      _icon(
        c,
        key: const Key('desk-toolbar-orientation'),
        icon: landscape
            ? Icons.stay_current_portrait_rounded
            : Icons.stay_current_landscape_rounded,
        tooltip: landscape ? '竖屏' : '横屏',
        on: landscape,
        onTap: onOrientation,
      ),
      _icon(
        c,
        key: const Key('desk-toolbar-keyboard'),
        icon: keyboardUp
            ? Icons.keyboard_hide_outlined
            : Icons.keyboard_outlined,
        tooltip: '键盘',
        on: keyboardUp,
        onTap: onKeyboard,
      ),
      _icon(
        c,
        key: const Key('desk-toolbar-shortcuts'),
        icon: Icons.keyboard_command_key_rounded,
        tooltip: '快捷键',
        on: shortcuts,
        onTap: onShortcuts,
      ),
      ValueListenableBuilder<bool>(
        valueListenable: zoomed,
        builder: (context, on, _) => _icon(
          c,
          key: const Key('desk-toolbar-zoom'),
          icon: Icons.fit_screen_rounded,
          tooltip: '实际大小',
          onTap: onZoom,
          dim: !on,
        ),
      ),
      _icon(
        c,
        key: const Key('desk-toolbar-debug'),
        icon: Icons.speed_rounded,
        tooltip: '调试',
        on: debugOpen,
        onTap: onDebug,
      ),
      _icon(
        c,
        key: const Key('desk-toolbar-disconnect'),
        icon: stopped ? Icons.refresh_rounded : Icons.link_off_rounded,
        tooltip: stopped ? '重新连接' : '断开',
        onTap: onDisconnect,
      ),
    ];
    return Padding(
      padding: EdgeInsets.symmetric(
        horizontal: landscape ? 2 : 4,
        vertical: landscape ? 4 : 2,
      ),
      child: landscape
          ? Column(mainAxisSize: MainAxisSize.min, children: icons)
          : Row(mainAxisSize: MainAxisSize.min, children: icons),
    );
  }

  /// 还没测到时不画满格，不然像是连接很好。
  (IconData, Color) _signal(HiveColors c) {
    if (latency == null && !unresponsive) {
      return (Icons.signal_cellular_alt, c.faint);
    }
    return (
      latencyIcon(latency, unresponsive: unresponsive),
      latencyColor(c, latency, unresponsive: unresponsive),
    );
  }

  Widget _icon(
    HiveColors c, {
    required Key key,
    required IconData icon,
    required String tooltip,
    required VoidCallback onTap,
    bool on = false,
    bool dim = false,
  }) {
    final color = dim ? c.faint : (on ? c.accent : c.text);
    return Tooltip(
      message: tooltip,
      child: Material(
        color: on ? c.tint : Colors.transparent,
        borderRadius: BorderRadius.circular(Rd.button),
        child: InkWell(
          key: key,
          onTap: onTap,
          borderRadius: BorderRadius.circular(Rd.button),
          child: SizedBox(
            width: 36,
            height: 32,
            child: Icon(icon, size: 20, color: color),
          ),
        ),
      ),
    );
  }
}
