import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/chat/send.dart';
import 'package:hive_mobile/terminal/frames.dart';

void main() {
  // 清空、命令、回车之间各隔 300ms：挨着发时 Claude Code 把回车当成粘贴的一部分，/exit 留在输入框里
  testWidgets('a slash command waits between clearing, typing and enter', (
    tester,
  ) async {
    final sent = <List<int>>[];
    final done = sendAgentCommand((raw) async {
      sent.add(raw);
      return SendOutcome.accepted;
    }, '/exit');
    await tester.pump();
    expect(sent, [clearLines(), clearLines()]);
    await tester.pump(const Duration(milliseconds: 299));
    expect(sent, hasLength(2));
    await tester.pump(const Duration(milliseconds: 1));
    expect(sent.last, '/exit'.codeUnits);
    await tester.pump(const Duration(milliseconds: 300));
    expect(sent.last, [0x0d]);
    expect(await done, SendOutcome.accepted);
  });

  testWidgets('a refused command is not followed by enter', (tester) async {
    final sent = <List<int>>[];
    final done = sendAgentCommand((raw) async {
      sent.add(raw);
      return raw.length == 5 ? SendOutcome.refused : SendOutcome.accepted;
    }, '/exit');
    await tester.pump(const Duration(seconds: 1));
    expect(sent, hasLength(3));
    expect(await done, SendOutcome.refused);
  });
}
