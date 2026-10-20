import 'package:flutter/material.dart';

import '../app_state.dart';
import '../ui/theme.dart';

/// 标题下面状态行末尾、上下文后面的延迟：信号格加和电脑的往返时间（[HiveAppModel.latency]）。
/// 经跨境隧道连电脑时几百毫秒很常见，超过 300 ms 变琥珀色；ping 超时、期间也没收到数据写「无响应」。
/// 还没测到时不占位置。
class LatencyLabel extends StatelessWidget {
  const LatencyLabel({super.key, this.latency, this.unresponsive = false});

  /// 读数，来自 [HiveAppModel.latency]、[HiveAppModel.unresponsive]。
  final Duration? latency;
  final bool unresponsive;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final latency = this.latency;
    if (latency == null && !unresponsive) return const SizedBox.shrink();
    final ms = latency?.inMilliseconds ?? 0;
    final color = unresponsive
        ? c.danger
        : ms >= 300
        ? c.honey
        : c.muted;
    final text = unresponsive ? '无响应' : '$ms ms';
    final icon = latencyIcon(latency, unresponsive: unresponsive);
    return Semantics(
      label: unresponsive ? '电脑没有响应' : '和电脑的延迟 $ms 毫秒',
      excludeSemantics: true,
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          const SizedBox(width: 10),
          Icon(icon, size: 12, color: color),
          const SizedBox(width: 3),
          Text(text, style: statusLineStyle(color)),
        ],
      ),
    );
  }
}

/// 信号格：一眼看出连接好不好，数字给要细看的人。150 ms 内满格，300 ms 起只剩一格（同时变琥珀色），
/// 没响应是带感叹号的空格。
IconData latencyIcon(Duration? latency, {bool unresponsive = false}) {
  if (unresponsive) return Icons.signal_cellular_connected_no_internet_0_bar;
  final ms = latency?.inMilliseconds ?? 0;
  if (ms < 150) return Icons.signal_cellular_alt;
  if (ms < 300) return Icons.signal_cellular_alt_2_bar;
  return Icons.signal_cellular_alt_1_bar;
}
