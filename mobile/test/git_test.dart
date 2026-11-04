import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/git/git.dart';
import 'package:posse_mobile/git/status.dart';
import 'package:posse_mobile/l10n/l10n.dart';

final zh = lookupAppLocalizations(const Locale('zh'));

void main() {
  test('status and diff parse the phone payload', () {
    final status = parseGitStatus({
      'branch': 'main',
      'upstream': 'origin/main',
      'ahead': 2,
      'behind': 0,
      'files': [
        {'path': 'a.txt', 'added': 3, 'deleted': 1},
        {'path': 'b.bin', 'binary': true, 'added': 0, 'deleted': 0},
      ],
    });
    expect(status.headline(zh), 'main  origin/main  领先 2');
    expect(status.files.first.stats(zh), '+3 -1');
    expect(status.files.last.stats(zh), '二进制');
    // 子模块和嵌套仓库（带斜杠）是目录，名字不带斜杠
    final dirs = parseGitStatus({
      'files': [
        {'path': 'third_party/sm', 'submodule': true},
        {'path': 'nested/', 'untracked': true},
      ],
    }).files;
    expect(dirs.map((f) => f.isDir), [true, true]);
    expect(dirs.last.name, 'nested');
    expect(dirs.last.dir, '');
    expect(dirs.first.name, 'sm');
    expect(gitWorktree('p1::w1'), 'id:p1::w1');
    expect(gitWorktree('id:p1::w1'), 'id:p1::w1');
    final diff = parseGitDiff({
      'path': 'a.txt',
      'diff': '+two',
      'truncated': true,
    });
    expect(diff.truncated, isTrue);
    expect(diff.diff, '+two');
  });

  test('diff rows carry line numbers, deleted lines the old ones', () {
    final rows = parseDiffRows(
      [
        'diff --git a/a.go b/a.go',
        'index 1..2 100644',
        '--- a/a.go',
        '+++ b/a.go',
        '@@ -10,3 +10,4 @@ func main() {',
        ' keep',
        '-old',
        '+new',
        '+more',
        r'\ No newline at end of file',
        '',
      ].join('\n'),
    );
    expect(
      [for (final r in rows) '${r.kind.name}:${r.number}:${r.text}'],
      [
        'hunk:10:func main() {',
        'context:10:keep',
        'deleted:11:old',
        'added:11:new',
        'added:12:more',
        'note:null:No newline at end of file',
      ],
    );
    // 认不出起始行号的段头照样显示内容，只是不标行号
    expect(
      [
        for (final r in parseDiffRows('@@\n+two\n'))
          '${r.kind.name}:${r.number}:${r.text}',
      ],
      ['hunk:null:', 'added:null:two'],
    );
  });

  Widget screen({
    required Map<String, Object?> file,
    void Function(String path)? onOpenFile,
  }) => MaterialApp(
    home: GitScreen(
      title: 'demo',
      loadStatus: () async => parseGitStatus({
        'branch': 'main',
        'files': [file],
      }),
      loadDiff: (file) async => parseGitDiff({
        'path': file.path,
        'diff': '@@ -1,1 +1,2 @@\n keep\n+two\n',
        'truncated': true,
      }),
      onOpenFile: onOpenFile,
    ),
  );

  testWidgets('file row opens the diff as a page like the file viewer', (
    tester,
  ) async {
    String? opened;
    await tester.pumpWidget(
      screen(
        file: {'path': 'src/a.txt', 'added': 1, 'deleted': 0},
        onOpenFile: (path) => opened = path,
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('a.txt'), findsOneWidget);
    expect(find.text('+1'), findsOneWidget);
    await tester.tap(find.text('a.txt'));
    await tester.pumpAndSettle();
    // 顶栏和文件页一样：文件名，下面是目录
    expect(find.text('src'), findsOneWidget);
    expect(find.text('two'), findsOneWidget);
    expect(find.text('第 1 行'), findsOneWidget);
    expect(find.text('差异太长，只显示前面一部分'), findsOneWidget);
    await tester.tap(find.byTooltip('查看完整文件'));
    expect(opened, 'src/a.txt');
  });

  testWidgets('every diff row is as wide as the longest one', (tester) async {
    final long = 'x' * 300;
    await tester.pumpWidget(
      MaterialApp(
        home: DiffViewer(
          file: parseGitStatus({
            'files': [
              {'path': 'a.txt', 'added': 1, 'deleted': 0},
            ],
          }).files.single,
          load: () async => parseGitDiff({
            'path': 'a.txt',
            'diff': '@@ -1,1 +1,2 @@\n keep\n+$long\n',
          }),
        ),
      ),
    );
    await tester.pumpAndSettle();
    double rowWidth(String text) => tester
        .getSize(
          find
              .ancestor(
                of: find.textContaining(text, findRichText: true),
                matching: find.byType(Container),
              )
              .first,
        )
        .width;
    expect(rowWidth('keep'), rowWidth(long));
    expect(rowWidth('keep'), greaterThan(800));
  });

  testWidgets('binary files say the diff cannot be shown', (tester) async {
    await tester.pumpWidget(screen(file: {'path': 'logo.png', 'binary': true}));
    await tester.pumpAndSettle();
    await tester.tap(find.text('logo.png'));
    await tester.pumpAndSettle();
    expect(find.text('二进制文件，手机上看不了差异'), findsOneWidget);
    expect(find.byTooltip('查看完整文件'), findsNothing);
  });

  test('changes fold into a directory tree, single-child dirs merged', () {
    final files = parseGitStatus({
      'files': [
        {'path': 'lib/src/ui/a.dart', 'x': '.', 'y': 'M'},
        {'path': 'lib/src/ui/b.dart', 'x': 'D', 'y': '.'},
        {'path': 'lib/main.dart', 'x': 'A', 'y': '.'},
        {'path': 'README.md', 'x': 'R', 'y': '.', 'orig': 'old.md'},
      ],
    }).files;
    String show(List<ChangeRow> rows) => [
      for (final r in rows)
        '${'  ' * r.depth}${r.file == null ? '${r.label}/ ${r.count}' : r.label}',
    ].join('\n');
    expect(
      show(changeTree(files)),
      'lib/ 3\n'
      '  src/ui/ 2\n'
      '    a.dart\n'
      '    b.dart\n'
      '  main.dart\n'
      'README.md',
    );
    expect(
      show(changeTree(files, folded: {'lib/src/ui'})),
      'lib/ 3\n  src/ui/ 2\n  main.dart\nREADME.md',
    );
    expect(
      [for (final f in files) f.kind],
      [
        ChangeKind.modified,
        ChangeKind.deleted,
        ChangeKind.added,
        ChangeKind.renamed,
      ],
    );
  });

  testWidgets('changes refresh on their own while the page is shown', (
    tester,
  ) async {
    var loads = 0;
    Widget pane(bool active) => MaterialApp(
      home: Scaffold(
        body: GitPane(
          active: active,
          loadStatus: () async {
            loads++;
            return parseGitStatus({
              'branch': 'main',
              'files': [
                {'path': 'v$loads.txt', 'x': '.', 'y': 'M'},
              ],
            });
          },
          loadDiff: (file) async => parseGitDiff({'path': file.path}),
        ),
      ),
    );
    await tester.pumpWidget(pane(true));
    await tester.pump();
    expect(find.text('v1.txt'), findsOneWidget);
    expect(find.byType(RefreshIndicator), findsNothing);
    await tester.pump(gitPoll);
    await tester.pump();
    expect(find.text('v2.txt'), findsOneWidget);
    // 切到文件页时不读
    await tester.pumpWidget(pane(false));
    await tester.pump(gitPoll * 2);
    expect(loads, 2);
    // 切回来马上读一次
    await tester.pumpWidget(pane(true));
    await tester.pump();
    expect(loads, 3);
    expect(find.text('v3.txt'), findsOneWidget);
  });

  testWidgets('changes list groups by kind and folds directories', (
    tester,
  ) async {
    await tester.pumpWidget(
      MaterialApp(
        home: GitScreen(
          title: 'demo',
          loadStatus: () async => parseGitStatus({
            'branch': 'main',
            'files': [
              {'path': 'src/a.txt', 'x': '.', 'y': 'M', 'added': 1},
              {'path': 'src/gone.txt', 'x': 'D', 'y': '.'},
              {'path': 'new.txt', 'x': '?', 'y': '?', 'untracked': true},
              {'path': 'shot.png', 'x': '?', 'y': '?', 'untracked': true},
              {
                'path': '${'very_long_name_' * 8}.txt',
                'x': '?',
                'y': '?',
                'untracked': true,
              },
              {
                'path': 'moved.txt',
                'x': 'R',
                'y': '.',
                'orig': 'src/moved.txt',
              },
              {
                'path': 'docs/new.md',
                'x': 'R',
                'y': '.',
                'orig': 'docs/old.md',
              },
            ],
          }),
          loadDiff: (file) async => parseGitDiff({'path': file.path}),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('改动'), findsOneWidget);
    expect(find.text('未跟踪'), findsOneWidget);
    // 不写状态字母，删除的文件名划掉
    expect(find.text('D'), findsNothing);
    expect(find.text('M'), findsNothing);
    expect(
      tester.widget<Text>(find.text('gone.txt')).style?.decoration,
      TextDecoration.lineThrough,
    );
    expect(find.text('a.txt'), findsOneWidget);
    // 未跟踪的图片也标图片，不写新文件
    expect(find.text('图片'), findsOneWidget);
    expect(find.text('新文件'), findsNothing);
    // 收起目录
    await tester.tap(find.text('src'));
    await tester.pumpAndSettle();
    expect(find.text('a.txt'), findsNothing);
    // 平铺时目录写在文件名后面
    await tester.tap(find.text('平铺'));
    await tester.pumpAndSettle();
    expect(find.text('src'), findsNWidgets(2));
    expect(find.text('docs  ← old.md'), findsOneWidget);
    expect(find.text('← src/moved.txt'), findsOneWidget);
    await tester.tap(find.text('按目录'));
    await tester.pumpAndSettle();
    expect(find.text('← old.md'), findsOneWidget);
    // 很长的文件名不撑破行
    expect(tester.takeException(), isNull);
  });
}
