import 'package:flutter/material.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../ui/loading.dart';
import '../workspace/models.dart';
import 'context_meter.dart';
import 'latency_label.dart';
import 'transcript.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';

/// 会话页和终端页共用的顶栏标题：agent 图标、标题，下面一行是状态；和电脑断开时这一行临时换成重连进度。
class SessionTitle extends StatelessWidget {
  const SessionTitle({
    super.key,
    required this.tab,
    this.model,
    this.ended = false,
  });

  final SessionTab tab;

  /// 给了就在 agent 图标右下角画连接状态的小圆点：绿色连着，琥珀色断开或正在重连。
  final PosseAppModel? model;

  @override
  Widget build(BuildContext context) {
    final model = this.model;
    if (model == null) {
      return _content(context, offline: false, connecting: false, ended: false);
    }
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) => _content(
        context,
        offline: model.rpc == null || model.connecting,
        connecting: model.connecting,
        ended: ended,
      ),
    );
  }

  /// 会话的终端已经不在了（关掉了，或者常驻进程重启过）。
  final bool ended;

  Widget _content(
    BuildContext context, {
    required bool offline,
    required bool connecting,
    required bool ended,
  }) {
    final c = PosseColors.of(context);
    // 断线时手上的状态已经过时，不显示；会话结束了也没有状态可言
    final state = offline || ended ? AgentState.none : tab.shown;
    final l = context.l10n;
    final stateText = ended
        ? l.chatStateEnded
        : switch (state) {
            AgentState.working =>
              tab.toolName == null
                  ? l.chatStateWorking
                  : l.chatStateUsingTool(tab.toolName!),
            AgentState.waiting => l.chatStateWaiting,
            AgentState.done || AgentState.unread => l.chatStateDone,
            AgentState.tasks => l.chatStateTasks,
            AgentState.starting => l.chatStateStarting,
            _ =>
              tab.agent == null
                  ? l.chatTerminal
                  : chatWho(l, 'assistant', tab.agent),
          };
    return Row(
      children: [
        _withLink(c, AgentTile(agent: tab.agent, size: Ic.large)),
        const SizedBox(width: 10),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                cleanTitle(tab.title),
                overflow: TextOverflow.ellipsis,
                style: TextStyle(
                  fontSize: Fs.title,
                  fontWeight: Fw.bold,
                  color: c.text,
                ),
              ),
              AnimatedSwitcher(
                duration: const Duration(milliseconds: 200),
                layoutBuilder: (current, previous) => Stack(
                  alignment: Alignment.centerLeft,
                  children: [...previous, ?current],
                ),
                child: offline && !ended
                    ? _reconnecting(l, c, connecting)
                    : _stateRow(context, c, l, state, stateText, ended),
              ),
            ],
          ),
        ),
      ],
    );
  }
}

extension on SessionTitle {
  /// 状态文字优先放全；后面的上下文、延迟、线路按顺序跟上，放不下的整项不显示，不把状态挤成省略号。
  Widget _stateRow(
    BuildContext context,
    PosseColors c,
    AppLocalizations l,
    AgentState state,
    String stateText,
    bool ended,
  ) {
    final model = this.model;
    final glyph = hasGlyph(state);
    final style = statusLineStyle(
      state == AgentState.waiting ? c.honey : c.muted,
    );
    // 一行字的高度：后面几项放进只有一行高的 Wrap，折到第二行的被裁掉
    final painter = TextPainter(
      text: TextSpan(text: stateText, style: style),
      textDirection: Directionality.of(context),
      textScaler: MediaQuery.textScalerOf(context),
      maxLines: 1,
    )..layout();
    final line = painter.height;
    painter.dispose();
    return LayoutBuilder(
      key: const ValueKey('state'),
      builder: (context, constraints) => Row(
        children: [
          // 没有图标（普通终端、看过的完成）时不留空位，文字和标题左边对齐
          if (glyph) ...[
            StatusHex(state: state, size: Ic.glyph),
            const SizedBox(width: 4),
          ],
          ConstrainedBox(
            constraints: BoxConstraints(
              maxWidth: constraints.maxWidth - (glyph ? Ic.glyph + 4 : 0),
            ),
            child: Text(
              stateText,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: style,
            ),
          ),
          if (model != null && !ended)
            Flexible(
              child: SizedBox(
                height: line,
                child: Wrap(
                  crossAxisAlignment: WrapCrossAlignment.center,
                  clipBehavior: Clip.hardEdge,
                  children: [
                    ContextMeter(tab: tab, model: model),
                    LatencyLabel(
                      latency: model.latency,
                      unresponsive: model.unresponsive,
                      gap: 12,
                    ),
                    if (routeLabel(l, model.connectedUrl) case final route?)
                      Padding(
                        padding: const EdgeInsets.only(left: 4),
                        child: Text(route, style: statusLineStyle(c.muted)),
                      ),
                  ],
                ),
              ),
            ),
        ],
      ),
    );
  }

  Widget _reconnecting(AppLocalizations l, PosseColors c, bool connecting) {
    return Row(
      key: const ValueKey('reconnecting'),
      children: [
        HexSpinner(size: Ic.glyph, color: c.honey),
        const SizedBox(width: 4),
        Flexible(
          child: Text(
            connecting ? l.chatConnecting : l.chatReconnecting,
            overflow: TextOverflow.ellipsis,
            style: statusLineStyle(c.honey),
          ),
        ),
      ],
    );
  }

  Widget _withLink(PosseColors c, Widget tile) {
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
                message: online
                    ? context.l10n.chatConnected
                    : context.l10n.chatDisconnected,
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
