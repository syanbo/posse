import 'package:flutter/material.dart';

import '../l10n/l10n.dart';
import '../ui/theme.dart';

/// 一种手势：做什么、怎么做。
typedef DeskGestureLine = ({IconData icon, String action, String how});

/// 手势提示：第一次进看桌面时盖一层，之后从悬浮球的「手势」再打开。手势和系统的相反。
class DeskGestureHint extends StatelessWidget {
  const DeskGestureHint({
    super.key,
    required this.onDismiss,
    this.trackpad = false,
  });

  final VoidCallback onDismiss;
  final bool trackpad;

  static List<DeskGestureLine> trackpadLines(AppLocalizations l) => [
    (
      icon: Icons.near_me_rounded,
      action: l.deskGestureMovePointer,
      how: l.deskGestureSlideOne,
    ),
    (
      icon: Icons.touch_app_rounded,
      action: l.deskGestureClick,
      how: l.deskGestureTapAtPointer,
    ),
    (
      icon: Icons.ads_click_rounded,
      action: l.deskGestureRightClick,
      how: l.deskGestureTwoFingerTap,
    ),
    (
      icon: Icons.open_with_rounded,
      action: l.deskGestureDrag,
      how: l.deskGestureThreeFingerDrag,
    ),
    (
      icon: Icons.swipe_vertical_rounded,
      action: l.deskGestureScroll,
      how: l.deskGestureTwoFingerDrag,
    ),
    (
      icon: Icons.pinch_rounded,
      action: l.deskGestureZoom,
      how: l.deskGesturePinch,
    ),
  ];

  static List<DeskGestureLine> lines(AppLocalizations l) => [
    (
      icon: Icons.touch_app_rounded,
      action: l.deskGestureClick,
      how: l.deskGestureTap,
    ),
    (
      icon: Icons.ads_click_rounded,
      action: l.deskGestureRightClick,
      how: l.deskGestureTwoFingerTap,
    ),
    (
      icon: Icons.open_with_rounded,
      action: l.deskGestureDrag,
      how: l.deskGestureLongPressDrag,
    ),
    (
      icon: Icons.pan_tool_rounded,
      action: l.deskGesturePan,
      how: l.deskGestureZoomedDrag,
    ),
    (
      icon: Icons.swipe_vertical_rounded,
      action: l.deskGestureScroll,
      how: l.deskGestureTwoFingerDrag,
    ),
    (
      icon: Icons.pinch_rounded,
      action: l.deskGestureZoom,
      how: l.deskGesturePinch,
    ),
  ];

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    return GestureDetector(
      key: const Key('desk-gesture-hint'),
      behavior: HitTestBehavior.opaque,
      onTap: onDismiss,
      child: ColoredBox(
        color: PosseColors.scrim,
        child: SafeArea(
          child: Center(
            child: SingleChildScrollView(
              padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 16),
              child: ConstrainedBox(
                constraints: const BoxConstraints(maxWidth: 360),
                child: Material(
                  color: c.raised,
                  borderRadius: BorderRadius.circular(Rd.card),
                  clipBehavior: Clip.antiAlias,
                  child: Padding(
                    padding: const EdgeInsets.all(20),
                    child: Column(
                      mainAxisSize: MainAxisSize.min,
                      crossAxisAlignment: CrossAxisAlignment.stretch,
                      children: [
                        _header(c, l),
                        const SizedBox(height: 14),
                        for (final line
                            in trackpad ? trackpadLines(l) : lines(l))
                          _row(c, line),
                        const SizedBox(height: 10),
                        Text(
                          l.deskGestureDismiss,
                          style: TextStyle(fontSize: Fs.small, color: c.muted),
                        ),
                      ],
                    ),
                  ),
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }

  Widget _header(PosseColors c, AppLocalizations l) {
    return Row(
      children: [
        Text(
          l.deskGestureTitle,
          style: TextStyle(
            fontSize: Fs.title,
            fontWeight: Fw.bold,
            color: c.text,
          ),
        ),
        const SizedBox(width: 8),
        DecoratedBox(
          decoration: BoxDecoration(
            color: c.tint,
            borderRadius: BorderRadius.circular(Rd.pill),
          ),
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
            child: Text(
              trackpad ? l.deskModeTrackpad : l.deskModeDirect,
              style: TextStyle(
                fontSize: Fs.small,
                fontWeight: Fw.medium,
                color: c.accent,
              ),
            ),
          ),
        ),
      ],
    );
  }

  Widget _row(PosseColors c, DeskGestureLine line) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 6),
      child: Row(
        children: [
          DecoratedBox(
            decoration: BoxDecoration(
              color: c.fill,
              borderRadius: BorderRadius.circular(Rd.button),
            ),
            child: SizedBox(
              width: 36,
              height: 36,
              child: Icon(line.icon, size: Ic.medium, color: c.text),
            ),
          ),
          const SizedBox(width: 12),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  line.action,
                  style: TextStyle(
                    fontSize: Fs.body,
                    fontWeight: Fw.medium,
                    color: c.text,
                  ),
                ),
                const SizedBox(height: 2),
                Text(
                  line.how,
                  style: TextStyle(fontSize: Fs.small, color: c.muted),
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}
