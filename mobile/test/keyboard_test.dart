import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/ui/keyboard.dart';

void main() {
  testWidgets('整页返回首页时收起键盘', (tester) async {
    final hides = <String>[];
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
      SystemChannels.textInput,
      (call) async {
        if (call.method == 'TextInput.hide') hides.add(call.method);
        return null;
      },
    );
    addTearDown(
      () => tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.textInput,
        null,
      ),
    );
    final nav = GlobalKey<NavigatorState>();
    await tester.pumpWidget(
      MaterialApp(
        navigatorKey: nav,
        navigatorObservers: [KeyboardDismisser()],
        home: const Scaffold(body: Text('首页')),
      ),
    );
    nav.currentState!.push(
      MaterialPageRoute<void>(
        builder: (_) => const Scaffold(body: TextField(autofocus: true)),
      ),
    );
    await tester.pumpAndSettle();
    final field = find.byType(EditableText);
    expect(
      tester.state<EditableTextState>(field).widget.focusNode.hasFocus,
      isTrue,
    );
    hides.clear();

    final focus = tester.state<EditableTextState>(field).widget.focusNode;
    nav.currentState!.pop();
    await tester.pump();
    expect(hides, isNotEmpty);
    expect(focus.hasFocus, isFalse);
    await tester.pumpAndSettle();
  });
}
