import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/app_state.dart';
import 'package:hive_mobile/chat/markdown_view.dart';
import 'package:hive_mobile/files/markdown_doc.dart';
import 'package:hive_mobile/ui/theme.dart';

const _sample = '''
# 一级标题 Hive

正文段落，带 **粗体**、*斜体*、~~删除线~~、`inline code` 和 [相对链接](../README.md)、[网址](https://example.com)、[打开 https://example.com](https://example.com)。

## 二级标题

- 第一项
- 第二项
  - 嵌套的子项
    - 再往下一层
- 第三项

  第三项里的第二段话。

1. 有序一
2. 有序二

- [ ] 没做完的任务
- [x] 做完的任务

### 三级标题

| 名称 | 说明 | 状态 |
|---|---|:-:|
| media | 图片与视频 | 完成 |
| latency | 只认请求的回复 | 待真机 |

> 引用第一段
>
> 引用第二段

---

```go
func main() {
	fmt.Println("hi")
}
```

#### 四级标题

![示意图](img/arch.png)
''';

Widget _app(Widget child, {bool dark = false}) => MaterialApp(
  debugShowCheckedModeBanner: false,
  theme: hiveTheme(dark ? Brightness.dark : Brightness.light),
  home: Scaffold(body: child),
);

MarkdownDoc _doc(HiveAppModel model, void Function(String) open) => MarkdownDoc(
  data: _sample,
  model: model,
  worktree: 'w1',
  path: 'docs/features/guide.md',
  openFile: open,
);

void main() {
  testWidgets('tables, task lists, nested lists and rules are drawn', (
    tester,
  ) async {
    final model = HiveAppModel(store: MemoryPairingStore());
    await tester.pumpWidget(_app(_doc(model, (_) {})));
    await tester.pump();

    expect(find.byType(Table), findsOneWidget);
    expect(find.byIcon(Icons.check_box_rounded), findsOneWidget);
    expect(find.byIcon(Icons.check_box_outline_blank), findsOneWidget);
    expect(find.text('◦'), findsOneWidget);
    expect(find.text('▪'), findsOneWidget);
    // 没连电脑时图片画不出来，退成替代文字；图片在文末，只建看得见的块，先滚过去
    final alt = find.textContaining('示意图', findRichText: true);
    await tester.scrollUntilVisible(
      alt,
      300,
      scrollable: find.byType(Scrollable).first,
    );
    expect(alt, findsOneWidget);
  });

  testWidgets('relative links open beside the markdown file', (tester) async {
    final model = HiveAppModel(store: MemoryPairingStore());
    final opened = <String>[];
    await tester.pumpWidget(_app(_doc(model, opened.add)));
    await tester.pump();

    await tester.tapOnText(find.textRange.ofSubstring('相对链接'));
    expect(opened, ['docs/README.md']);
    // 链接文字里带网址时外面的链接不能坏，原文的方括号不该露出来
    expect(find.textContaining('](https://', findRichText: true), findsNothing);
  });

  // 离屏截图对比新旧渲染，给人看：
  // HIVE_SHOTS_DIR=/tmp/shots fvm flutter test test/markdown_doc_test.dart
  final dir = Platform.environment['HIVE_SHOTS_DIR'] ?? '';
  testWidgets('markdown screenshots', (tester) async {
    final root = Platform.environment['FLUTTER_ROOT']!;
    final fonts = {
      'Roboto': '/Library/Fonts/Arial Unicode.ttf',
      'MaterialIcons':
          '$root/bin/cache/artifacts/material_fonts/MaterialIcons-Regular.otf',
    };
    await tester.runAsync(() async {
      for (final e in fonts.entries) {
        final bytes = File(e.value).readAsBytesSync();
        await (FontLoader(
          e.key,
        )..addFont(Future.value(ByteData.sublistView(bytes)))).load();
      }
    });
    await tester.binding.setSurfaceSize(const Size(390, 1500));
    final model = HiveAppModel(store: MemoryPairingStore());
    for (final dark in [false, true]) {
      final suffix = dark ? 'dark' : 'light';
      for (final (name, child) in [
        ('new', _doc(model, (_) {}) as Widget),
        (
          'old',
          Builder(
            builder: (context) => MarkdownView(
              _sample,
              style: TextStyle(
                fontSize: Fs.read,
                height: 1.6,
                color: HiveColors.of(context).text,
              ),
            ),
          ),
        ),
      ]) {
        final key = GlobalKey();
        await tester.pumpWidget(
          RepaintBoundary(
            key: key,
            child: _app(child, dark: dark),
          ),
        );
        await tester.pumpAndSettle();
        await tester.runAsync(() async {
          final boundary =
              key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
          final image = await boundary.toImage(pixelRatio: 2);
          final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
          File('$dir/md-$name-$suffix.png')
              .writeAsBytesSync(bytes!.buffer.asUint8List());
        });
      }
    }
  }, skip: dir.isEmpty);
}
