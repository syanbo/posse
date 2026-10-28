import 'package:flutter/material.dart';

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

  static const trackpadLines = <DeskGestureLine>[
    (icon: Icons.near_me_outlined, action: '移动指针', how: '单指滑动'),
    (icon: Icons.touch_app_outlined, action: '单击', how: '单指轻点，点在指针处'),
    (icon: Icons.ads_click_rounded, action: '右键', how: '双指轻点'),
    (icon: Icons.open_with_rounded, action: '拖动', how: '三指滑动，或轻点后马上按住滑'),
    (icon: Icons.swipe_vertical_outlined, action: '滚动', how: '双指拖动'),
    (icon: Icons.pinch_outlined, action: '缩放', how: '双指捏合'),
  ];

  static const lines = <DeskGestureLine>[
    (icon: Icons.touch_app_outlined, action: '单击', how: '单指轻点'),
    (icon: Icons.ads_click_rounded, action: '右键', how: '双指轻点'),
    (icon: Icons.open_with_rounded, action: '拖动', how: '长按到震一下，再拖'),
    (icon: Icons.pan_tool_outlined, action: '移动画面', how: '放大后单指拖动'),
    (icon: Icons.swipe_vertical_outlined, action: '滚动', how: '双指拖动'),
    (icon: Icons.pinch_outlined, action: '缩放', how: '双指捏合'),
  ];

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return GestureDetector(
      key: const Key('desk-gesture-hint'),
      behavior: HitTestBehavior.opaque,
      onTap: onDismiss,
      child: ColoredBox(
        color: const Color(0x99000000),
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
                    padding: const EdgeInsets.fromLTRB(20, 20, 20, 18),
                    child: Column(
                      mainAxisSize: MainAxisSize.min,
                      crossAxisAlignment: CrossAxisAlignment.stretch,
                      children: [
                        _header(c),
                        const SizedBox(height: 14),
                        for (final line in trackpad ? trackpadLines : lines)
                          _row(c, line),
                        const SizedBox(height: 10),
                        Text(
                          '点任意处关掉 · 模式在设置「看桌面」里换',
                          style: TextStyle(
                            fontSize: Fs.caption,
                            color: c.faint,
                          ),
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

  Widget _header(PosseColors c) {
    return Row(
      children: [
        Text(
          '手势',
          style: TextStyle(
            fontSize: Fs.cardTitle,
            fontWeight: FontWeight.w600,
            color: c.text,
          ),
        ),
        const SizedBox(width: 8),
        DecoratedBox(
          decoration: BoxDecoration(
            color: c.tint,
            borderRadius: BorderRadius.circular(999),
          ),
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
            child: Text(
              trackpad ? '触控板模式' : '直接点按模式',
              style: TextStyle(
                fontSize: Fs.caption,
                fontWeight: FontWeight.w500,
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
              child: Icon(line.icon, size: 20, color: c.text),
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
                    fontWeight: FontWeight.w500,
                    color: c.text,
                  ),
                ),
                const SizedBox(height: 1),
                Text(
                  line.how,
                  style: TextStyle(fontSize: Fs.meta, color: c.muted),
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}
