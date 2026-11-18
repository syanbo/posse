import 'dart:async';

import 'package:flutter/material.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../protocol/rpc.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../workspace/models.dart';
import 'send.dart';
import 'transcript.dart' show chatWho;

/// 读最后一次请求的上下文：Claude/Codex 是百分比，Antigravity 是 token 数，没有读数时为空。
typedef ContextReader = Future<int?> Function(SessionTab tab);

Future<int?> readSessionContext(PhoneRpc rpc, SessionTab tab) async {
  final res = await rpc.call('session.context', {
    'agent': tab.agent,
    'sessionId': tab.sessionId,
    if (tab.transcriptPath != null) 'transcriptPath': tab.transcriptPath,
  });
  if (res['known'] != true) return null;
  return (res['percent'] as num?)?.toInt();
}

/// 往终端里写一串按键。测试里替换成记下来。
typedef ContextSender = Future<SendOutcome> Function(
  SessionTab tab,
  List<int> raw,
);

/// 标题下面状态行末尾的上下文用量：小圆环加「42%」，圆环已经说明是用量，不再写字。手机终端太窄，Claude 自己状态栏里的百分比常被截掉。
/// Claude、Codex 显示百分比，Antigravity 只有 token 数；每 15 秒和每次状态变化时重读，电脑上的 posse 太旧（没有这个接口）就不再问。
/// 点它问要不要压缩，确认后清空输入框、发 /compact（两家都认）。
class ContextMeter extends StatefulWidget {
  const ContextMeter({
    super.key,
    required this.tab,
    required this.model,
    this.read,
    this.send,
  });

  final SessionTab tab;
  final PosseAppModel model;

  /// 测试里替换读数的来源。
  final ContextReader? read;

  /// 测试里替换写终端。
  final ContextSender? send;

  @override
  State<ContextMeter> createState() => _ContextMeterState();
}

class _ContextMeterState extends State<ContextMeter> {
  int? _percent;
  int? _tokens;
  Timer? _timer;
  var _unsupported = false;

  bool get _applies =>
      const {'claude', 'codex', 'antigravity'}.contains(widget.tab.agent) &&
      (widget.tab.sessionId?.isNotEmpty ?? false);

  @override
  void initState() {
    super.initState();
    _refresh();
    _timer = Timer.periodic(const Duration(seconds: 15), (_) {
      if (!appAway) _refresh();
    });
  }

  @override
  void didUpdateWidget(ContextMeter old) {
    super.didUpdateWidget(old);
    // 一轮做完、换了会话时用量才会变
    if (old.tab.state != widget.tab.state ||
        old.tab.sessionId != widget.tab.sessionId ||
        old.tab.agent != widget.tab.agent) {
      if (old.tab.sessionId != widget.tab.sessionId ||
          old.tab.agent != widget.tab.agent) {
        _percent = null;
        _tokens = null;
      }
      _refresh();
    }
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }

  Future<void> _refresh() async {
    if (_unsupported || !_applies) return;
    final read = widget.read;
    final rpc = widget.model.rpc;
    if (read == null && rpc == null) return;
    final tab = widget.tab;
    try {
      if (tab.agent == 'antigravity') {
        int? tokens;
        if (read != null) {
          tokens = await read(tab);
        } else {
          final res = await rpc!.call('session.context', {
            'agent': tab.agent,
            'sessionId': tab.sessionId,
            if (tab.transcriptPath != null)
              'transcriptPath': tab.transcriptPath,
          });
          tokens = res['known'] == true ? (res['used'] as num?)?.toInt() : null;
        }
        if (mounted &&
            tab.sessionId == widget.tab.sessionId &&
            tab.agent == widget.tab.agent) {
          setState(() {
            _tokens = tokens;
            _percent = null;
          });
        }
        return;
      }
      final percent = await (read ?? (t) => readSessionContext(rpc!, t))(tab);
      if (mounted &&
          tab.sessionId == widget.tab.sessionId &&
          tab.agent == widget.tab.agent) {
        setState(() => _percent = percent);
      }
    } on RpcException catch (e) {
      if (e.missingMethod) _unsupported = true;
    } catch (_) {
      // 断线之类，下一轮再读
    }
  }

  Future<void> _compact() async {
    final percent = _percent;
    if (percent == null) return;
    final l = context.l10n;
    final who = chatWho(l, 'assistant', widget.tab.agent);
    final ok = await showPosseConfirm(
      context,
      title: l.chatCompactConfirm,
      body: l.chatCompactBody(percent, who),
      confirm: l.chatCompactButton,
    );
    if (!ok || !mounted) return;
    final rpc = widget.model.rpc;
    final send =
        widget.send ??
        (rpc == null
            ? null
            : (SessionTab tab, List<int> raw) => sendTerminal(
                rpc,
                terminal: tab.terminalId,
                raw: raw,
                enter: false,
              ));
    if (send == null) return;
    final tab = widget.tab;
    final outcome = await sendAgentCommand((raw) => send(tab, raw), '/compact');
    final note = sendOutcomeText(l, outcome);
    if (!mounted || note.isEmpty) return;
    ScaffoldMessenger.maybeOf(context)
        ?.showSnackBar(SnackBar(content: Text(note)));
  }

  @override
  Widget build(BuildContext context) {
    final percent = _percent;
    if (!_applies) return const SizedBox.shrink();
    final tokens = _tokens;
    if (widget.tab.agent == 'antigravity') {
      if (tokens == null) return const SizedBox.shrink();
      return Padding(
        padding: const EdgeInsets.only(left: statusLineGap),
        child: Text(
          context.l10n.chatContextTokens(tokens),
          style: statusLineStyle(PosseColors.of(context).muted),
        ),
      );
    }
    if (percent == null) return const SizedBox.shrink();
    final c = PosseColors.of(context);
    // 快满时提醒：Claude 到 80% 左右会自动压缩
    final color = percent >= 90
        ? c.danger
        : percent >= 75
        ? c.honey
        : c.muted;
    // 前面的间距放在这里：没有读数时整个不占位置，后面的延迟不会多空一截
    return Semantics(
      label: context.l10n.chatContextUsed(percent),
      button: true,
      excludeSemantics: true,
      child: GestureDetector(
        key: const Key('context-meter'),
        behavior: HitTestBehavior.opaque,
        onTap: () => unawaited(_compact()),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            const SizedBox(width: 12),
            SizedBox.square(
              dimension: 10,
              // 粗细同后面的信号格：12 号图标里一格宽 1.5
              child: CircularProgressIndicator(
                value: percent / 100,
                strokeWidth: 1.5,
                color: color,
                backgroundColor: c.fill,
              ),
            ),
            const SizedBox(width: 4),
            Text('$percent%', style: statusLineStyle(color)),
          ],
        ),
      ),
    );
  }
}
