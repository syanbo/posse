import 'dart:convert';
import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/discussions/api.dart';
import 'package:posse_mobile/discussions/discussions.dart';
import 'package:posse_mobile/discussions/room.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/workspace/models.dart';

import 'shot_fonts.dart';

// 讨论列表、讨论页（协作讨论、@ 候选、详情、成员表单）的离屏截图，给人看排版用：
// POSSE_MOBILE_PREVIEW_DIR=/tmp/shots fvm flutter test test/discussions_shots_test.dart

final _png = base64Decode(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==',
);

DateTime _at(int minutes) =>
    DateTime.now().subtract(Duration(minutes: minutes)).toUtc();

class _Rpc implements PhoneRpc {
  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    final disc = {
      'key': 'room',
      'title': '重做登录流程',
      'description': '去掉验证码弹窗，记住密码用钥匙串。',
      'status': 'active',
      'shared': true,
      'rev': 9,
      'msgSeq': 9,
    };
    switch (method) {
      case 'discussion.list':
        return {
          'self': 'self',
          'seq': 3,
          'items': [
            {
              ...disc,
              'mentionSeq': 9,
              'last': {
                'authorName': 'Hana',
                'body': '@Mo 方案我看过了，钥匙串那段你来补一下',
                'createdAt': _at(3).toIso8601String(),
              },
            },
            {
              'key': 'b',
              'title': 'IM 重连路径梳理',
              'status': 'active',
              'msgSeq': 4,
              'questions': 1,
              'last': {
                'authorName': 'Codex 审查',
                'body': '飞行模式恢复后会重复建连',
                'createdAt': _at(60 * 26).toIso8601String(),
              },
            },
            {
              'key': 'd',
              'title': '旧的发版检查',
              'status': 'archived',
              'msgSeq': 1,
              'last': {'body': '好了', 'createdAt': _at(9000).toIso8601String()},
            },
            {
              'key': 'c',
              'title': '安卓 qa 包打包失败',
              'status': 'active',
              'msgSeq': 2,
              'last': {
                'authorName': 'Grok',
                'body': 'JDK 版本不对，已经说明。',
                'createdAt': _at(60 * 50).toIso8601String(),
              },
            },
          ],
        };
      case 'discussion.search':
        return {
          'value': [
            {
              'id': 'q',
              'authorName': 'Claude 方案',
              'kind': 'question',
              'body': '钥匙串的 access group 用主 App 的还是共享的？',
              'createdAt': _at(10).toIso8601String(),
            },
            {
              'id': 'h',
              'authorName': 'Hana',
              'authorId': 'hana',
              'kind': 'user',
              'body': '@Mo 方案我看过了，钥匙串那段你来补一下',
              'createdAt': _at(3).toIso8601String(),
            },
          ],
        };
      case 'discussion.attachment.thumb':
        return {'content': base64Encode(_png), 'width': 1600, 'height': 1000};
      case 'discussion.show':
        return {
          'discussion': disc,
          'collab': {
            'self': 'self',
            'state': 'active',
            'connected': true,
            'people': [
              {'fp': 'self', 'name': 'Mo', 'state': 'active', 'online': true},
              {
                'fp': 'hana',
                'name': 'Hana',
                'state': 'active',
                'host': true,
                'online': true,
              },
              {'fp': 'lin', 'name': 'Lin', 'state': 'active'},
            ],
          },
          'members': [
            {
              'id': 'plan',
              'name': 'Claude 方案',
              'agent': 'claude',
              'model': 'opus',
              'role': '出方案、写实现',
              'owner': 'self',
              'access': 'hana',
            },
            {
              'id': 'review',
              'name': 'Codex 审查',
              'agent': 'codex',
              'model': 'gpt-5.5',
              'role': '复核方案和代码',
              'owner': 'hana',
              'access': 'all',
            },
            {'id': 'ops', 'name': 'Grok 运维', 'agent': 'grok', 'owner': 'lin'},
          ],
          'messages': [
            {
              'id': 'm1',
              'seq': 1,
              'kind': 'user',
              'authorId': 'hana',
              'authorName': 'Hana',
              'body': '这周把登录页重做一下：去掉验证码弹窗，记住密码用钥匙串。先各自看看方案。',
              'createdAt': _at(80).toIso8601String(),
              'keyAt': _at(70).toIso8601String(),
            },
            {
              'id': 'm2',
              'seq': 2,
              'kind': 'user',
              'authorId': 'self',
              'authorName': 'Mo',
              'body': '先读一下 LoginView.swift，列出要改的地方，截图是现在的样子。',
              'target': 'plan',
              'targetName': 'Claude 方案',
              'attachments': [
                {
                  'id': 'a1',
                  'name': 'login.png',
                  'type': 'image/png',
                  'size': 245000,
                },
              ],
              'createdAt': _at(60).toIso8601String(),
            },
            {
              'id': 'm3',
              'seq': 3,
              'kind': 'reply',
              'authorMember': 'plan',
              'authorAgent': 'claude',
              'authorName': 'Claude 方案',
              'body': '要改三处：\n\n- `LoginView` 去掉 `CaptchaSheet`\n- 新增 `KeychainStore` 存密码\n- 登录成功后写入钥匙串',
              'hasReport': true,
              'execution': 'x1',
              'createdAt': _at(50).toIso8601String(),
            },
            {
              'id': 'm4',
              'seq': 4,
              'kind': 'user',
              'authorId': 'self',
              'authorName': 'Mo',
              'quote': 'm3',
              'body': '照这个改，顺便把单测补上。',
              'target': 'plan',
              'targetName': 'Claude 方案',
              'createdAt': _at(20).toIso8601String(),
            },
            {
              'id': 'm5',
              'seq': 5,
              'kind': 'question',
              'authorMember': 'plan',
              'authorAgent': 'claude',
              'authorName': 'Claude 方案',
              'body': '钥匙串的 access group 用主 App 的还是共享的？',
              'question': 'q1',
              'execution': 'x2',
              'createdAt': _at(10).toIso8601String(),
            },
            {
              'id': 'm6',
              'seq': 6,
              'kind': 'user',
              'authorId': 'hana',
              'authorName': 'Hana',
              'body': '方案我看过了，钥匙串那段你来补一下',
              'mention': 'self',
              'mentionName': 'Mo',
              'createdAt': _at(3).toIso8601String(),
            },
          ],
          'executions': [
            {
              'id': 'x1',
              'message': 'm2',
              'member': 'plan',
              'state': 'done',
              'owner': 'self',
            },
            {
              'id': 'x2',
              'message': 'm4',
              'member': 'plan',
              'state': 'waiting',
              'owner': 'self',
              'terminal': true,
            },
            {
              'id': 'x3',
              'message': 'm6',
              'member': 'review',
              'state': 'running',
              'owner': 'hana',
            },
          ],
          'key': [
            {
              'id': 'm1',
              'authorName': 'Hana',
              'body': '这周把登录页重做一下：去掉验证码弹窗，记住密码用钥匙串。',
            },
          ],
          'more': false,
          'moreAfter': false,
        };
    }
    return {};
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class _Model extends PosseAppModel {
  _Model()
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());
  final fake = _Rpc();
  @override
  List<String> get agents => const ['claude', 'codex', 'grok', 'cursor'];
  @override
  List<WorktreeRow> get worktrees => const [
    WorktreeRow(
      id: 'p::main',
      repo: 'posse',
      name: 'posse',
      path: '/posse',
      branch: 'main',
      status: '',
      live: 0,
      isMain: true,
    ),
  ];
  @override
  PhoneRpc? get rpc => fake;
  @override
  String? get activeId => 'test-computer';
}

Future<void> _shot(WidgetTester t, GlobalKey key, String name) async {
  final dir = Platform.environment['POSSE_MOBILE_PREVIEW_DIR'];
  if (dir == null) return;
  await t.runAsync(() async {
    final image =
        await (key.currentContext!.findRenderObject()! as RenderRepaintBoundary)
            .toImage(pixelRatio: 2);
    final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
    await Directory(dir).create(recursive: true);
    await File('$dir/$name.png').writeAsBytes(bytes!.buffer.asUint8List());
    image.dispose();
  });
}

/// 执行中的转圈一直在动，pumpAndSettle 等不到停；按时间推几帧，等弹出动画放完。
Future<void> _settle(WidgetTester t) async {
  for (var i = 0; i < 10; i++) {
    await t.pump(const Duration(milliseconds: 100));
  }
}

void main() {
  setUp(() => SharedPreferences.setMockInitialValues({}));
  for (final brightness in Brightness.values) {
    final b = brightness.name;
    testWidgets('discussion shots $b', (t) async {
      if (Platform.environment.containsKey('POSSE_MOBILE_PREVIEW_DIR')) {
        await loadShotFonts(t);
      }
      t.view.physicalSize = const Size(390, 844);
      t.view.devicePixelRatio = 1;
      addTearDown(t.view.reset);
      final model = _Model();
      final key = GlobalKey();
      final base = posseTheme(brightness);
      Widget app(Widget home) => MaterialApp(
        theme: shotTheme(base).copyWith(
          appBarTheme: base.appBarTheme.copyWith(
            titleTextStyle: base.appBarTheme.titleTextStyle!.copyWith(
              fontFamily: 'Roboto',
            ),
          ),
        ),
        locale: const Locale('zh'),
        localizationsDelegates: AppLocalizations.localizationsDelegates,
        supportedLocales: AppLocalizations.supportedLocales,
        builder: (_, child) => RepaintBoundary(key: key, child: child),
        home: home,
      );

      await t.pumpWidget(app(DiscussionsScreen(model: model)));
      await t.pumpAndSettle();
      expect(t.takeException(), isNull);
      expect(find.text('@你'), findsOneWidget);
      await _shot(t, key, 'disc-list-$b');

      await t.pumpWidget(
        app(
          DiscussionRoom(api: DiscussionApi(model), room: 'room', self: 'self'),
        ),
      );
      await _settle(t);
      expect(t.takeException(), isNull);
      await _shot(t, key, 'disc-room-$b');
      await t.drag(find.byType(ListView).first, const Offset(0, 2000));
      await _settle(t);
      await _shot(t, key, 'disc-room-top-$b');

      await t.enterText(find.byType(TextField), '@');
      await _settle(t);
      // 别人放开给所有人的 Agent 能直接 @；没放开的提醒它的拥有者
      expect(find.text('提醒 Lin 安排'), findsOneWidget);
      await _shot(t, key, 'disc-at-$b');
      await t.tap(find.text('Codex 审查').last);
      await _settle(t);
      expect(find.text('@Codex 审查'), findsOneWidget);
      await t.enterText(find.byType(TextField), '');
      await _settle(t);

      await t.tap(find.byTooltip('讨论详情'));
      await _settle(t);
      expect(t.takeException(), isNull);
      await _shot(t, key, 'disc-details-$b');

      // 点自己的 Agent 直接进编辑页，打开终端、重启、移除在页面下面
      await t.tap(find.text('Claude 方案').last);
      await _settle(t);
      expect(find.text('谁能 @ 它'), findsOneWidget);
      await _shot(t, key, 'disc-member-$b');
      await t.tap(find.text('新建 worktree'));
      await _settle(t);
      await _shot(t, key, 'disc-member-worktree-$b');
      await t.binding.handlePopRoute();
      await _settle(t);
      await t.binding.handlePopRoute();
      await _settle(t);

      // 回答进输入框；搜索在顶栏
      await t.drag(find.byType(ListView).first, const Offset(0, -3000));
      await _settle(t);
      await t.tap(find.text('回答'));
      await _settle(t);
      expect(find.text('回答 Claude 方案'), findsOneWidget);
      await _shot(t, key, 'disc-answer-$b');
      await t.tap(find.byTooltip('搜索消息'));
      await _settle(t);
      await t.enterText(find.byType(TextField).first, '钥匙串');
      await t.testTextInput.receiveAction(TextInputAction.search);
      await _settle(t);
      await _shot(t, key, 'disc-search-$b');

      await t.pumpWidget(const SizedBox());
      await t.pumpAndSettle();
      model.dispose();
    });
  }
}
