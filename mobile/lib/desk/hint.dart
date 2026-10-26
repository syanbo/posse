import 'package:flutter/material.dart';

import '../ui/theme.dart';

/// 第一次进看桌面时盖一层。手势和系统的相反，只说一次。
class DeskGestureHint extends StatelessWidget {
  const DeskGestureHint({
    super.key,
    required this.onDismiss,
    this.trackpad = false,
  });

  final VoidCallback onDismiss;
  final bool trackpad;

  static const trackpadLines = <(String, String)>[
    ('单指滑动', '移动指针'),
    ('单指点按', '在指针处单击'),
    ('双指轻点', '右键'),
    ('轻点后按住滑', '拖动'),
    ('双指拖动', '滚动'),
    ('捏合', '缩放'),
    ('三指横滑', '切换空间'),
  ];

  static const lines = <(String, String)>[
    ('单指点按', '单击'),
    ('双指轻点', '右键'),
    ('长按拖动', '按住左键'),
    ('双指拖动', '滚动'),
    ('捏合', '缩放'),
    ('三指横滑', '切换空间'),
  ];

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return GestureDetector(
      key: const Key('desk-gesture-hint'),
      behavior: HitTestBehavior.opaque,
      onTap: onDismiss,
      child: ColoredBox(
        color: const Color(0x8C000000),
        child: SafeArea(
          child: Center(
            child: SingleChildScrollView(
              padding: const EdgeInsets.symmetric(horizontal: 28, vertical: 16),
              child: Material(
                color: c.card,
                borderRadius: BorderRadius.circular(Rd.card),
                child: Padding(
                  padding: const EdgeInsets.fromLTRB(18, 16, 18, 14),
                  child: Column(
                    mainAxisSize: MainAxisSize.min,
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      Text(
                        '手势',
                        style: TextStyle(
                          fontSize: Fs.cardTitle,
                          fontWeight: FontWeight.w600,
                          color: c.text,
                        ),
                      ),
                      const SizedBox(height: 10),
                      for (final line in trackpad ? trackpadLines : lines)
                        Padding(
                          padding: const EdgeInsets.symmetric(vertical: 3),
                          child: Row(
                            children: [
                              SizedBox(
                                width: 88,
                                child: Text(
                                  line.$1,
                                  style: TextStyle(
                                    fontSize: Fs.body,
                                    color: c.text,
                                  ),
                                ),
                              ),
                              Text(
                                line.$2,
                                style: TextStyle(
                                  fontSize: Fs.body,
                                  color: c.muted,
                                ),
                              ),
                            ],
                          ),
                        ),
                      const SizedBox(height: 8),
                      Text(
                        '点任意处关掉',
                        style: TextStyle(fontSize: Fs.meta, color: c.faint),
                      ),
                    ],
                  ),
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }
}
