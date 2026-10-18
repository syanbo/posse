import 'dart:convert';

import '../protocol/rpc.dart';

enum SendOutcome { accepted, refused, unknown }

/// [raw] 是终端模拟器要写进 PTY 的原始字节。对话输入走 [text]，由服务端按 UTF-8 写入。
/// 带 [raw] 时也按 UTF-8 解出一份 text：不认 raw 的旧版 Hive 照样收得到按键。
Future<SendOutcome> sendTerminal(
  PhoneRpc rpc, {
  required String terminal,
  String text = '',
  List<int>? raw,
  required bool enter,
}) async {
  try {
    final result = await rpc.call('terminal.send', {
      'terminal': terminal,
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
