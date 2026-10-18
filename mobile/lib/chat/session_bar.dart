import 'package:flutter/material.dart';

import '../app_state.dart';
import '../workspace/models.dart';
import 'transcript.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';

/// 会话页和终端页共用的顶栏标题：agent 图标、标题，下面一行是状态。
class SessionTitle extends StatelessWidget {
  const SessionTitle({
    super.key,
    required this.tab,
    this.model,
    this.ended = false,
  });

  final SessionTab tab;

  /// 给了就在 agent 图标右下角画连接状态的小圆点：绿色连着，琥珀色断开或正在重连。
  final HiveAppModel? model;

  @override
  Widget build(BuildContext context) {
    final model = this.model;
    if (model == null) return _content(context, offline: false, ended: false);
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) => _content(
        context,
        offline: model.rpc == null || model.connecting,
        ended: ended,
      ),
    );
  }

  /// 会话的终端已经不在了（关掉了，或者常驻进程重启过）。
  final bool ended;

  Widget _content(
    BuildContext context, {
    required bool offline,
    required bool ended,
  }) {
    final c = HiveColors.of(context);
    // 断线时手上的状态已经过时，不显示；会话结束了也没有状态可言
    final state = offline || ended ? AgentState.none : agentStateOf(tab.state);
    final stateText = ended
        ? '会话已经结束'
        : offline
        ? '等连上电脑'
        : switch (state) {
            AgentState.working =>
              tab.toolName == null ? '工作中' : '正在用 ${tab.toolName}',
            AgentState.waiting => '等你处理',
            AgentState.done => '本轮完成',
            _ => tab.agent == null ? '终端' : chatWho('assistant', tab.agent),
          };
    return Row(
      children: [
        _withLink(c, AgentTile(agent: tab.agent, size: 28)),
        const SizedBox(width: 10),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                cleanTitle(tab.title),
                overflow: TextOverflow.ellipsis,
                style: TextStyle(
                  fontSize: 16,
                  fontWeight: FontWeight.w600,
                  color: c.text,
                ),
              ),
              Row(
                children: [
                  // 没有状态（普通终端）时不留空位，文字和标题左边对齐
                  if (state != AgentState.none) ...[
                    StatusHex(state: state, size: 11),
                    const SizedBox(width: 4),
                  ],
                  Flexible(
                    child: Text(
                      stateText,
                      overflow: TextOverflow.ellipsis,
                      style: TextStyle(
                        fontSize: 12,
                        fontWeight: FontWeight.w400,
                        color: state == AgentState.waiting ? c.honey : c.muted,
                      ),
                    ),
                  ),
                ],
              ),
            ],
          ),
        ),
      ],
    );
  }
}

extension on SessionTitle {
  Widget _withLink(HiveColors c, Widget tile) {
    final model = this.model;
    if (model == null) return tile;
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) {
        final online = model.rpc != null && !model.connecting;
        return Stack(
          clipBehavior: Clip.none,
          children: [
            tile,
            Positioned(
              right: -3,
              bottom: -3,
              child: Tooltip(
                message: online ? '已连接电脑' : '和电脑的连接断开了',
                child: Container(
                  width: 11,
                  height: 11,
                  decoration: BoxDecoration(
                    color: online ? c.sage : c.honey,
                    shape: BoxShape.circle,
                    border: Border.all(color: c.canvas, width: 2),
                  ),
                ),
              ),
            ),
          ],
        );
      },
    );
  }
}

/// 和电脑断开、正在重连时，顶栏下面的一条提示。连着时不占地方。
class ConnectionBanner extends StatelessWidget {
  const ConnectionBanner({super.key, required this.model});

  final HiveAppModel model;

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) {
        if (model.rpc != null && !model.connecting) {
          return const SizedBox.shrink();
        }
        final c = HiveColors.of(context);
        final name = model.current?.label ?? '电脑';
        return Container(
          width: double.infinity,
          color: c.honey.withValues(alpha: c.dark ? 0.14 : 0.10),
          padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
          child: Row(
            children: [
              SizedBox.square(
                dimension: 14,
                child: CircularProgressIndicator(
                  strokeWidth: 1.8,
                  color: c.honey,
                ),
              ),
              const SizedBox(width: 10),
              Expanded(
                child: Text(
                  model.connecting ? '正在连接 $name…' : '和 $name 的连接断开了，正在重连',
                  style: TextStyle(fontSize: 13, color: c.honey),
                ),
              ),
            ],
          ),
        );
      },
    );
  }
}
