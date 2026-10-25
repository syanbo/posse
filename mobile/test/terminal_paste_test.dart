import 'package:flterm/flterm.dart';
// ignore: implementation_imports
import 'package:flterm/src/widgets/terminal_input_client.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  test('pasting drops the trailing newlines so it is not sent', () {
    expect(trimPastedNewlines('echo hi\n'), 'echo hi');
    expect(trimPastedNewlines('a\nb\r\n\n'), 'a\nb');
    expect(trimPastedNewlines('a\nb'), 'a\nb');
  });

  test('a multi-line insert from the keyboard is a paste, not keys', () {
    final client = TerminalInputClient();
    final typed = <String>[];
    final pasted = <String>[];
    var enters = 0;
    client
      ..onTextCommitted = typed.add
      ..onNewline = (() => enters++)
      ..onPasted = pasted.add;
    const before = TextEditingValue(
      text: ' ',
      selection: TextSelection.collapsed(offset: 1),
    );
    client.updateEditingValueWithDeltas([
      const TextEditingDeltaInsertion(
        oldText: ' ',
        textInserted: 'ls -la\n',
        insertionOffset: 1,
        selection: TextSelection.collapsed(offset: 8),
        composing: TextRange.empty,
      ),
    ]);
    expect(pasted, ['ls -la\n']);
    expect(typed, isEmpty);
    expect(enters, 0);
    // 键盘上的回车照旧是回车
    client.updateEditingValueWithDeltas([
      TextEditingDeltaInsertion(
        oldText: before.text,
        textInserted: '\n',
        insertionOffset: 1,
        selection: const TextSelection.collapsed(offset: 2),
        composing: TextRange.empty,
      ),
    ]);
    expect(enters, 1);
  });
}
