import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/git/git.dart';
import 'package:hive_mobile/git/status.dart';

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
    expect(status.headline, 'main  origin/main  领先 2');
    expect(status.files.first.stats, '+3 -1');
    expect(status.files.last.stats, '二进制');
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
              .ancestor(of: find.text(text), matching: find.byType(Container))
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
}
