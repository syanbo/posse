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

  testWidgets('file row opens the diff drawer', (tester) async {
    await tester.pumpWidget(
      MaterialApp(
        home: GitScreen(
          title: 'demo',
          loadStatus: () async => parseGitStatus({
            'branch': 'main',
            'files': [
              {'path': 'a.txt', 'added': 1, 'deleted': 0},
            ],
          }),
          loadDiff: (path) async => parseGitDiff({
            'path': path,
            'diff': '@@\n+two\n',
            'truncated': true,
          }),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('a.txt'), findsOneWidget);
    expect(find.text('+1'), findsOneWidget);
    await tester.tap(find.text('a.txt'));
    await tester.pumpAndSettle();
    expect(find.textContaining('+two'), findsOneWidget);
    expect(find.text('差异太长，只显示前面一部分'), findsOneWidget);
  });
}
