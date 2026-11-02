import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/chat/markdown_view.dart';
import 'package:posse_mobile/files/markdown_doc.dart';
import 'package:posse_mobile/ui/theme.dart';

const _sample = '''
# 一级标题 Posse

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
  theme: posseTheme(dark ? Brightness.dark : Brightness.light),
  home: Scaffold(body: child),
);

// 一格里是一整句话的表格：列宽封顶，句子在格子里折行
const _longTable = '''
| 阶段 | 做什么 | 客户端 | 目的 |
| :-- | :-- | :-- | :-- |
| A | Posse 常驻进程实现 Orca 的 `nativeChat.*` 协议，补齐 agent 状态行里手机要的字段 | 现有 Orca App（iOS / Android） | 不写一行客户端代码就能验证"结构化对话 + 审批卡片"是否够用，协议顺带定稿 |
| B | Flutter 客户端，复用阶段 A 的协议，再加 Posse 自己的扩展（hook 审批决策、多地址竞速、Git 速览） | 自研，iOS 先、Android 后期 | 摆脱 Orca App 的节奏和 UI 限制 |
''';

// README 常见的 HTML：居中的标题和徽章、折叠、图文表格、格子里空了行写的 Markdown
const _html = '''
<p align="center">
  <img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==" width="40" alt="标志">
  <h1 align="center">居中标题</h1>
</p>

按 <kbd>Ctrl</kbd>+<kbd>C</kbd> 复制，<b>加粗</b>换行<br>第二行 <!-- 注释 -->

<details>
<summary>展开看看</summary>

折起来的 **内容**

</details>

<table>
<tr><th>名称</th><th>说明</th></tr>
<tr><td>甲</td><td>第一项</td></tr>
</table>

<table>
<tr>
<td width="50%">

### 格子里的标题

- 列表项

</td>
<td><img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==" alt="右图"></td>
</tr>
</table>

<script>alert("不显示")</script>

结尾段落
''';

MarkdownDoc _doc(
  PosseAppModel model,
  void Function(String) open, {
  String data = _sample,
}) => MarkdownDoc(
  data: data,
  model: model,
  worktree: 'w1',
  path: 'docs/features/guide.md',
  openFile: open,
);

void main() {
  testWidgets('tables, task lists, nested lists and rules are drawn', (
    tester,
  ) async {
    final model = PosseAppModel(store: MemoryPairingStore());
    await tester.pumpWidget(_app(_doc(model, (_) {})));
    await tester.pump();

    expect(find.byType(Table), findsOneWidget);
    expect(find.byIcon(Icons.check_box_rounded), findsOneWidget);
    expect(find.byIcon(Icons.check_box_outline_blank_rounded), findsOneWidget);
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
    final model = PosseAppModel(store: MemoryPairingStore());
    final opened = <String>[];
    await tester.pumpWidget(_app(_doc(model, opened.add)));
    await tester.pump();

    await tester.tapOnText(find.textRange.ofSubstring('相对链接'));
    expect(opened, ['docs/README.md']);
    // 链接文字里带网址时外面的链接不能坏，原文的方括号不该露出来
    expect(find.textContaining('](https://', findRichText: true), findsNothing);
  });

  testWidgets('long table cells wrap inside a capped column', (tester) async {
    await tester.binding.setSurfaceSize(const Size(390, 900));
    final model = PosseAppModel(store: MemoryPairingStore());
    await tester.pumpWidget(_app(_doc(model, (_) {}, data: _longTable)));
    await tester.pump();
    final cell = find.textContaining('补齐 agent 状态行', findRichText: true);
    expect(cell, findsOneWidget);
    final size = tester.getSize(cell);
    expect(size.width, lessThanOrEqualTo(220));
    // 折成了好几行，而不是一行拉到屏幕外
    expect(size.height, greaterThan(40));
    // 表头和第一列都在屏幕里
    expect(tester.getTopLeft(find.text('做什么')).dx, lessThan(390));
  });

  testWidgets('table scrollbar is not lifted by the bottom safe area', (
    tester,
  ) async {
    final model = PosseAppModel(store: MemoryPairingStore());
    await tester.pumpWidget(
      MediaQuery(
        data: const MediaQueryData(padding: EdgeInsets.only(bottom: 34)),
        child: _app(_doc(model, (_) {}, data: _longTable)),
      ),
    );
    await tester.pump();
    // iOS 的横向滚动条按 MediaQuery 的底部安全区抬高，表格里要看到 0
    final ctx = tester.element(find.byType(Table));
    expect(MediaQuery.paddingOf(ctx).bottom, 0);
  });

  testWidgets('html in markdown is interpreted like GitHub', (tester) async {
    await tester.binding.setSurfaceSize(const Size(390, 1600));
    final model = PosseAppModel(store: MemoryPairingStore());
    await tester.pumpWidget(_app(_doc(model, (_) {}, data: _html)));
    await tester.pumpAndSettle();

    // 标签不原样露出来，注释和 <script> 的内容不显示
    expect(find.textContaining('<', findRichText: true), findsNothing);
    expect(find.textContaining('注释', findRichText: true), findsNothing);
    expect(find.textContaining('不显示', findRichText: true), findsNothing);
    expect(find.textContaining('结尾段落', findRichText: true), findsOneWidget);

    // 居中的标题
    final title = find.textContaining('居中标题', findRichText: true);
    expect(title, findsOneWidget);
    final center = tester.getCenter(title).dx;
    expect(center, closeTo(390 / 2, 20));

    // data: 地址的图按写的宽度画
    final logo = find.byWidgetPredicate((w) => w is Image && w.width == 40);
    expect(logo, findsOneWidget);

    // 行内标签：<kbd> 是行内代码，<br> 换行
    expect(find.textContaining('Ctrl', findRichText: true), findsOneWidget);
    expect(find.textContaining('换行\n第二行', findRichText: true), findsOneWidget);

    // <details> 默认收着，点 summary 展开
    final folded = find.textContaining('折起来的', findRichText: true);
    expect(folded, findsNothing);
    await tester.tap(find.textContaining('展开看看', findRichText: true));
    await tester.pumpAndSettle();
    expect(folded, findsOneWidget);

    // 只有文字、第一行是 <th> 的表格照 Markdown 表格画；带图和块的是 HTML 表格，
    // 格子里空了行写的 Markdown 挂回格子
    expect(find.byType(Table), findsNWidgets(2));
    final cellTitle = find.textContaining('格子里的标题', findRichText: true);
    expect(cellTitle, findsOneWidget);
    expect(
      find.ancestor(of: cellTitle, matching: find.byType(Table)),
      findsOneWidget,
    );
    expect(
      find.byWidgetPredicate((w) => w is Image && w.width == null),
      findsOneWidget,
    );
  });

  testWidgets('ragged html tables and tags inside dropped content', (
    tester,
  ) async {
    final model = PosseAppModel(store: MemoryPairingStore());
    const data = '''
<table>
<tr><th>甲</th><th>乙</th></tr>
<tr><td>只有一格</td></tr>
</table>

前文 <svg><a href="x"><text>图里的字</text></svg> 后文
''';
    await tester.pumpWidget(_app(_doc(model, (_) {}, data: data)));
    await tester.pump();
    expect(tester.takeException(), isNull);
    expect(find.textContaining('只有一格', findRichText: true), findsOneWidget);
    expect(find.textContaining('图里的字', findRichText: true), findsNothing);
    expect(find.textContaining('后文', findRichText: true), findsOneWidget);
  });

  test('image sources follow GitHub', () {
    MdImageSource? src(String s) =>
        mdImageSource(s, worktree: 'w1', docPath: 'docs/guide.md');
    expect(src('img/a.png')!.path, 'docs/img/a.png');
    expect(src('../logo.png?raw=true')!.path, 'logo.png');
    expect(src('/design/icon.png')!.path, 'design/icon.png');
    expect(src('https://img.shields.io/badge/a-b-green')!.url, isNotNull);
    expect(src('data:image/png;base64,AAAA')!.url, startsWith('data:'));
    expect(src('../../outside.png'), isNull);
    expect(src('ftp://x/y.png'), isNull);
  });

  // 离屏截图对比新旧渲染，给人看：
  // POSSE_SHOTS_DIR=/tmp/shots fvm flutter test test/markdown_doc_test.dart
  final dir = Platform.environment['POSSE_SHOTS_DIR'] ?? '';
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
    final model = PosseAppModel(store: MemoryPairingStore());
    for (final dark in [false, true]) {
      final suffix = dark ? 'dark' : 'light';
      for (final (name, child) in [
        ('new', _doc(model, (_) {}) as Widget),
        ('table', _doc(model, (_) {}, data: _longTable)),
        ('html', _doc(model, (_) {}, data: _html)),
        (
          'old',
          Builder(
            builder: (context) => MarkdownView(
              _sample,
              style: TextStyle(
                fontSize: Fs.body,
                height: Lh.read,
                color: PosseColors.of(context).text,
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
