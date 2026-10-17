import '../protocol/rpc.dart';

enum SendOutcome { accepted, refused, unknown }

Future<SendOutcome> sendTerminal(
  PhoneRpc rpc, {
  required String terminal,
  required String text,
  required bool enter,
}) async {
  try {
    final result = await rpc.call('terminal.send', {
      'terminal': terminal,
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
