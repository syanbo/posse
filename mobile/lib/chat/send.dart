import 'dart:convert';

import '../protocol/rpc.dart';
import '../terminal/frames.dart';

enum SendOutcome { accepted, refused, unknown }

/// [raw] 是终端模拟器要写进 PTY 的原始字节。对话输入走 [text]，由服务端按 UTF-8 写入。
/// 带 [raw] 时也按 UTF-8 解出一份 text：不认 raw 的旧版 Posse 照样收得到按键。
/// [keys] 是一串按键，电脑一个一个写、中间隔一小会儿（`rawKeys`）；不认它的旧版 Posse 按 raw 一次写。
Future<SendOutcome> sendTerminal(
  PhoneRpc rpc, {
  required String terminal,
  String text = '',
  List<int>? raw,
  List<List<int>>? keys,
  required bool enter,
}) async {
  if (keys != null) raw = [for (final k in keys) ...k];
  try {
    final result = await rpc.call('terminal.send', {
      'terminal': terminal,
      if (keys != null) 'rawKeys': [for (final k in keys) base64.encode(k)],
      if (raw != null) ...{
        'raw': base64.encode(raw),
        'text': utf8.decode(raw, allowMalformed: true),
      } else
        'text': text,
      'enter': enter,
      'requireWriteSettlement': true,
    });
    final send = result['send'];
    if (send is! Map) return SendOutcome.unknown;
    final settlement = send['writeSettlement'];
    final outcome = settlement is Map ? settlement['outcome'] : null;
    if (send['accepted'] == true && outcome == 'accepted') {
      return SendOutcome.accepted;
    }
    if (send['accepted'] == false || outcome == 'refused') {
      return SendOutcome.refused;
    }
    return SendOutcome.unknown;
  } catch (_) {
    return SendOutcome.unknown;
  }
}

String sendOutcomeText(SendOutcome outcome) => switch (outcome) {
  SendOutcome.accepted => '',
  SendOutcome.refused => '没写进去',
  SendOutcome.unknown => '未确认，先看聊天再重试',
};

/// 清空 agent 的输入框，再执行一条斜杠命令（/compact）。[write] 往终端里写一串按键。
/// 三步之间各隔 [gap]：挨得太近时 Claude Code 把回车和前面的字当成同一次粘贴，命令留在输入框里不执行
/// （实测清空后马上写命令、120ms 后回车，斜杠菜单开着、没提交）。
Future<SendOutcome> sendAgentCommand(
  Future<SendOutcome> Function(List<int> raw) write,
  String command, {
  Duration gap = const Duration(milliseconds: 300),
}) async {
  await write(clearLines());
  await write(clearLines());
  await Future<void>.delayed(gap);
  final typed = await write(utf8.encode(command));
  if (typed == SendOutcome.refused) return typed;
  await Future<void>.delayed(gap);
  return write(const [0x0d]);
}
