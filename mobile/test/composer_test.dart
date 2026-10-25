import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/chat/composer.dart';
import 'package:posse_mobile/ui/theme.dart';

void main() {
  testWidgets('single-line composer stays compact', (tester) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: Scaffold(
          bottomNavigationBar: ChatComposer(
            controller: TextEditingController(),
            images: const [],
            sending: false,
            connected: true,
            ended: false,
            hint: '回复',
            onChanged: (_) {},
            onSubmit: () {},
            onPickImage: () {},
            onRemoveImage: (_) {},
            onPaste: () {},
            onContentInserted: (_) {},
          ),
        ),
      ),
    );
    final image = tester.getSize(find.byTooltip('发送图片'));
    expect(image.height, 34);
    // 框（含上下 4 的内边距和描边）加外边距 2 + 6
    expect(
      tester.getSize(find.byType(ChatComposer)).height,
      lessThanOrEqualTo(52),
    );
  });
}
