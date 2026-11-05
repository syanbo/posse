import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/git/branches.dart';
import 'package:posse_mobile/git/git.dart';
import 'package:posse_mobile/git/status.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/protocol/rpc.dart';

final zh = lookupAppLocalizations(const Locale('zh'));

final _payload = {
  'head': 'main',
  'operation': '',
  'branches': [
    {'name': 'main', 'current': true, 'time': 10, 'upstream': 'origin/main'},
    {'name': 'old', 'time': 1},
    {'name': 'feat', 'time': 5, 'elsewhere': '/w/feat', 'workspace': 'p1::w2'},
    {'name': 'origin/main', 'remote': true, 'time': 10},
    {'name': 'origin/new', 'remote': true, 'time': 20},
    {'name': 'origin/older', 'remote': true, 'time': 2},
  ],
};

void main() {
  test('local current first, remotes without a local twin, newest first', () {
    final list = parseGitBranches(_payload);
    final (local, remote) = branchGroups(list.branches, '');
    expect(local.map((b) => b.name), ['main', 'feat', 'old']);
    expect(remote.map((b) => b.name), ['origin/new', 'origin/older']);
    expect(list.branches[2].workspace, 'p1::w2');
    final (l2, r2) = branchGroups(list.branches, 'NEW');
    expect(l2, isEmpty);
    expect(r2.map((b) => b.name), ['origin/new']);
  });

  testWidgets('tap the branch card, confirm while an agent works, switch', (
    tester,
  ) async {
    final calls = <String>[];
    var opened = '';
    await tester.pumpWidget(
      MaterialApp(
        locale: const Locale('zh'),
        localizationsDelegates: AppLocalizations.localizationsDelegates,
        supportedLocales: AppLocalizations.supportedLocales,
        home: Scaffold(
          body: GitPane(
            loadStatus: () async =>
                parseGitStatus({'branch': 'main', 'files': []}),
            loadDiff: (file) async => parseGitDiff({'path': file.path}),
            branches: GitBranchActions(
              load: (repo) async => parseGitBranches(_payload),
              checkout: (repo, b, force) async {
                calls.add('${b.name}:$force');
                return force
                    ? const GitCheckoutResult(switched: true, head: 'new')
                    : const GitCheckoutResult(
                        switched: false,
                        confirm: 'codex',
                      );
              },
              workspaceName: (id) => id == 'p1::w2' ? 'demo / feat' : null,
              openWorkspace: (id) => opened = id,
            ),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text('main'));
    await tester.pumpAndSettle();
    expect(find.text(zh.gitBranchesTitle), findsOneWidget);
    expect(find.text(zh.gitBranchInWorkspace('demo / feat')), findsOneWidget);
    // 本地已有同名分支的远端分支不列
    expect(find.text('origin/main'), findsNothing);

    await tester.tap(find.text('origin/new'));
    await tester.pumpAndSettle();
    expect(find.text(zh.gitCheckoutAgentTitle('Codex')), findsOneWidget);
    await tester.tap(find.text(zh.gitCheckoutAnyway));
    await tester.pumpAndSettle();
    expect(calls, ['origin/new:false', 'origin/new:true']);
    expect(find.text(zh.gitBranchesTitle), findsNothing);
    expect(find.text(zh.gitSwitchedTo('new')), findsOneWidget);

    // 别的工作区检出着的：跳过去，不切换
    await tester.tap(find.text('main'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('feat'));
    await tester.pumpAndSettle();
    expect(opened, 'p1::w2');
    expect(calls.length, 2);
  });

  Widget pane({
    required Future<GitBranchList> Function() load,
    required Future<GitCheckoutResult> Function(GitBranch b) checkout,
  }) => MaterialApp(
    locale: const Locale('zh'),
    localizationsDelegates: AppLocalizations.localizationsDelegates,
    supportedLocales: AppLocalizations.supportedLocales,
    home: Scaffold(
      body: GitPane(
        loadStatus: () async => parseGitStatus({'branch': 'main', 'files': []}),
        loadDiff: (file) async => parseGitDiff({'path': file.path}),
        branches: GitBranchActions(
          load: (repo) => load(),
          checkout: (repo, b, force) => checkout(b),
          workspaceName: (id) => null,
        ),
      ),
    ),
  );

  testWidgets('a failed switch keeps its error after the list reloads', (
    tester,
  ) async {
    var loads = 0;
    await tester.pumpWidget(
      pane(
        load: () async {
          loads++;
          return parseGitBranches(_payload);
        },
        checkout: (b) async =>
            throw RpcException('git', 'error: would be overwritten'),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text('main'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('old'));
    await tester.pumpAndSettle();
    expect(loads, 2);
    expect(find.textContaining('would be overwritten'), findsOneWidget);
    expect(find.text(zh.gitBranchesTitle), findsOneWidget);
  });

  testWidgets('a timed-out switch that did happen counts as switched', (
    tester,
  ) async {
    var head = 'main';
    await tester.pumpWidget(
      pane(
        load: () async => parseGitBranches({..._payload, 'head': head}),
        checkout: (b) async {
          head = b.localName;
          throw TimeoutException('git.checkout');
        },
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text('main'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('origin/new'));
    await tester.pumpAndSettle();
    expect(find.text(zh.gitBranchesTitle), findsNothing);
    expect(find.text(zh.gitSwitchedTo('new')), findsOneWidget);
  });
}
