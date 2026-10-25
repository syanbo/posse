import 'package:flutter/material.dart';

import '../app_state.dart';
import '../ui/theme.dart';

/// 和电脑的往返时间（[PosseAppModel.latency]）：信号格加毫秒数，不写单位，省出胶囊里电脑名的位置。
/// 满格绿色，两格灰色，一格（300 ms 起，经跨境隧道连电脑时常见）琥珀色；ping 超时、期间也没收到数据
/// 是红色空格加「无响应」。还没测到时不占位置。会话页状态行末尾和首页电脑胶囊里都用它。
class LatencyLabel extends StatelessWidget {
  const LatencyLabel({
    super.key,
    this.latency,
    this.unresponsive = false,
    this.gap = 10,
    this.size = 12,
  });

  /// 读数，来自 [PosseAppModel.latency]、[PosseAppModel.unresponsive]。
  final Duration? latency;
  final bool unresponsive;

  /// 前面空出的宽度：状态行里和上下文隔开，胶囊里顶替圆点时不要。
  final double gap;
  final double size;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final latency = this.latency;
    if (latency == null && !unresponsive) return const SizedBox.shrink();
    final ms = latency?.inMilliseconds ?? 0;
    final color = latencyColor(c, latency, unresponsive: unresponsive);
    return Semantics(
      label: unresponsive ? '电脑没有响应' : '和电脑的延迟 $ms 毫秒',
      excludeSemantics: true,
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          SizedBox(width: gap),
          Icon(
            latencyIcon(latency, unresponsive: unresponsive),
            size: size,
            color: color,
          ),
          const SizedBox(width: 3),
          Text(unresponsive ? '无响应' : '$ms', style: statusLineStyle(color)),
        ],
      ),
    );
  }
}

/// 信号格的颜色：满格绿色，两格灰色，一格琥珀色，没响应红色。
Color latencyColor(
  PosseColors c,
  Duration? latency, {
  bool unresponsive = false,
}) {
  if (unresponsive) return c.danger;
  final ms = latency?.inMilliseconds ?? 0;
  if (ms < 150) return c.sage;
  if (ms < 300) return c.muted;
  return c.honey;
}

/// 信号格：一眼看出连接好不好。150 ms 内满格，300 ms 起只剩一格，
/// 没响应是带感叹号的空格。
IconData latencyIcon(Duration? latency, {bool unresponsive = false}) {
  if (unresponsive) return Icons.signal_cellular_connected_no_internet_0_bar;
  final ms = latency?.inMilliseconds ?? 0;
  if (ms < 150) return Icons.signal_cellular_alt;
  if (ms < 300) return Icons.signal_cellular_alt_2_bar;
  return Icons.signal_cellular_alt_1_bar;
}
