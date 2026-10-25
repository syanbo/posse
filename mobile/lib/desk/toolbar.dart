import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';

import '../chat/latency_label.dart';
import '../ui/theme.dart';
import 'chrome.dart';

const _fade = Duration(milliseconds: 160);

/// 悬浮球：在父组件给的范围里上下左右都能拖，松手吸到最近的左边或右边。空的地方不吃点击。
class DeskBarSlot extends StatefulWidget {
  const DeskBarSlot({
    super.key,
    required this.anchor,
    required this.onDrag,
    required this.child,
  });

  /// 两个方向各自 0 靠起点、1 靠终点。x 松手后只会是 0 或 1。
  final ValueNotifier<Offset> anchor;

  /// 拖动时报一次，按住时 x 也跟手；松手吸边由这里做，父组件只管记活动时间。
  final VoidCallback onDrag;
  final Widget child;

  @override
  State<DeskBarSlot> createState() => _DeskBarSlotState();
}

class _DeskBarSlotState extends State<DeskBarSlot> {
  final _key = GlobalKey();
  var _dragging = false;

  /// 拖动当时量。展开动画的第一帧量过就会把轨道算窄，接着拖不跟手。
  Size _size() {
    final box = _key.currentContext?.findRenderObject() as RenderBox?;
    if (box == null || !box.hasSize) return Size.zero;
    return box.size;
  }

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(
      builder: (context, constraints) {
        return ValueListenableBuilder<Offset>(
          valueListenable: widget.anchor,
          builder: (context, anchor, child) {
            return AnimatedAlign(
              duration: _dragging ? Duration.zero : _snap,
              curve: Curves.easeOutCubic,
              alignment: Alignment(
                deskBarAlignment(anchor.dx),
                deskBarAlignment(anchor.dy),
              ),
              child: child,
            );
          },
          child: GestureDetector(
            key: _key,
            onPanStart: (_) => setState(() => _dragging = true),
            onPanUpdate: (details) {
              final size = _size();
              final a = widget.anchor.value;
              widget.anchor.value = Offset(
                (a.dx +
                        details.delta.dx /
                            deskBarTrack(constraints.maxWidth, size.width))
                    .clamp(0.0, 1.0),
                (a.dy +
                        details.delta.dy /
                            deskBarTrack(constraints.maxHeight, size.height))
                    .clamp(0.0, 1.0),
              );
              widget.onDrag();
            },
            onPanEnd: (_) => _settle(),
            onPanCancel: _settle,
            child: widget.child,
          ),
        );
      },
    );
  }

  void _settle() {
    final a = widget.anchor.value;
    setState(() => _dragging = false);
    widget.anchor.value = Offset(a.dx < 0.5 ? 0 : 1, a.dy);
  }
}

const _snap = Duration(milliseconds: 220);

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
    this.onView,
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

  /// 选看哪块屏或哪个窗口。电脑还没报屏（或太旧不报）时为空，按钮不出现。
  final VoidCallback? onView;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Material(
      key: const Key('desk-toolbar'),
      color: c.card.withValues(alpha: 0.94),
      borderRadius: BorderRadius.circular(999),
      clipBehavior: Clip.antiAlias,
      child: AnimatedSize(
        duration: _fade,
        curve: Curves.easeOutCubic,
        alignment: Alignment.center,
        // 没画面时没什么可挡的，按钮一直摆出来，返回和横屏不用先找胶囊点开
        child: expanded || !video ? _open(c) : _shut(c),
      ),
    );
  }

  Widget _shut(PosseColors c) {
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
        padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 8),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [icon, const SizedBox(height: 4), label],
        ),
      ),
    );
  }

  Widget _open(PosseColors c) {
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
      if (onView case final pick?)
        _icon(
          c,
          key: const Key('desk-toolbar-view'),
          icon: Icons.desktop_windows_outlined,
          tooltip: '画面',
          onTap: pick,
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
      padding: const EdgeInsets.symmetric(horizontal: 2, vertical: 4),
      child: Column(mainAxisSize: MainAxisSize.min, children: icons),
    );
  }

  /// 还没测到时不画满格，不然像是连接很好。
  (IconData, Color) _signal(PosseColors c) {
    if (latency == null && !unresponsive) {
      return (Icons.signal_cellular_alt, c.faint);
    }
    return (
      latencyIcon(latency, unresponsive: unresponsive),
      latencyColor(c, latency, unresponsive: unresponsive),
    );
  }

  Widget _icon(
    PosseColors c, {
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
