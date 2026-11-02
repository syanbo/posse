import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/chat/chat.dart';
import 'package:posse_mobile/chat/composer.dart';
import 'package:posse_mobile/chat/echo.dart';
import 'package:posse_mobile/chat/media.dart';
import 'package:posse_mobile/chat/prompt.dart';
import 'package:posse_mobile/chat/tools.dart';
import 'package:posse_mobile/files/files.dart';
import 'package:posse_mobile/files/save.dart';
import 'package:posse_mobile/git/git.dart';
import 'package:posse_mobile/git/status.dart';
import 'package:posse_mobile/history/history.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/workspace/models.dart';

import 'shot_fonts.dart';

// 对话、技能与工具、图片与视频、历史、Git、文件几页的离屏截图，中文和英文各一套，给人看排版用：
// POSSE_SHOTS_DIR=/tmp/shots fvm flutter test test/chat_shots_test.dart

final _png = base64Decode(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==',
);

class _Rpc implements PhoneRpc {
  final chat = StreamController<Map<String, Object?>>();
  final tabs = StreamController<Map<String, Object?>>();

  @override
  Future<RpcStream> subscribe(
    String method,
    Map<String, Object?> params,
  ) async {
    final c = method == 'nativeChat.subscribe' ? chat : tabs;
    return RpcStream(method, c.stream, () async {});
  }

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    final now = DateTime.now();
    switch (method) {
      case 'session.media':
        return {
          'made': [
            {'id': 'g1', 'kind': 'image', 'label': '', 'tag': 'generated'},
            {'id': 'c1', 'kind': 'image', 'label': '', 'tag': 'codeOutput'},
            {'id': 'v1', 'kind': 'video', 'label': 'rec.mp4', 'size': 10},
          ],
          'madeAll': 12,
          'pasted': [
            {'id': 'p1', 'kind': 'image', 'label': '', 'tag': 'pasted'},
          ],
        };
      case 'session.mediaThumb':
        return {'content': base64Encode(_png), 'width': 1, 'height': 1};
      case 'history.list':
        return {
          'sessions': [
            for (final (title, agent, ago) in [
              ('0412｜修复｜手机端登录失败', 'claude', const Duration(minutes: 5)),
              ('', 'codex', const Duration(days: 1)),
              ('0401｜重构｜Git 面板', 'cursor', const Duration(days: 4)),
              ('0320｜调研｜多语言方案', 'grok', const Duration(days: 40)),
            ])
              {
                'agent': agent,
                'id': title,
                'title': title,
                'cwd': '/Users/x/posse',
                'branch': 'main',
                'updatedAt': now.subtract(ago).toUtc().toIso8601String(),
              },
          ],
        };
      case 'fs.listDir':
        return {
          'value': [
            {'name': 'lib', 'isDirectory': true},
            {'name': 'README.md', 'isDirectory': false},
          ],
        };
    }
    return {};
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class _Model extends PosseAppModel {
  _Model(this.fake)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());

  final _Rpc fake;

  @override
  PhoneRpc? get rpc => fake;
}

const _tab = SessionTab(
  worktreeId: 'w',
  title: 'Fix login on the phone',
  terminalId: 't1',
  agent: 'claude',
  state: 'working',
  sessionId: 's1',
);

Map<String, Object?> _msg(
  String id,
  String role,
  List<Map<String, Object?>> blocks,
  int offset,
) => {
  'id': id,
  'role': role,
  'source': 'transcript',
  'transcriptOffset': offset,
  'timestamp': DateTime(2026, 4, 12, 9, 30).millisecondsSinceEpoch,
  'blocks': blocks,
};

final _usage = ToolUsage.fromJson({
  'found': true,
  'skills': [
    {'name': 'archify', 'count': 2},
  ],
  'mcp': [
    {'server': 'iphone-use', 'count': 3},
  ],
  'cli': [
    {'name': 'git', 'source': 'homebrew', 'count': 4},
    {'name': 'flutter', 'count': 1},
    {'name': 'xcrun', 'source': 'system', 'count': 1},
  ],
  'web': [
    {'host': 'github.com', 'count': 1},
  ],
  'searches': 2,
  'events': [
    {'at': '2026-10-28T01:00:00Z', 'skill': 'archify'},
    {'at': '2026-10-28T01:01:00Z', 'server': 'iphone-use', 'tool': 'phone_tap'},
    {
      'at': '2026-10-28T01:02:00Z',
      'server': 'iphone-use',
      'tool': 'phone_tap',
      'failed': true,
    },
    {'cli': 'xcrun', 'source': 'system', 'command': 'xcrun simctl list'},
    {'cli': 'flutter', 'command': 'flutter test'},
    {
      'at': '2026-10-28T01:04:00Z',
      'url': 'https://www.github.com/a/b?x=1',
      'via': 'user',
    },
    {
      'at': '2026-10-28T01:05:00Z',
      'query': 'flutter sliver',
      'via': 'WebSearch',
    },
  ],
});

final _status = parseGitStatus({
  'branch': 'feature/mobile-i18n',
  'upstream': 'origin/feature/mobile-i18n',
  'ahead': 2,
  'behind': 1,
  'files': [
    {'path': 'lib/chat/chat.dart', 'added': 12, 'deleted': 3},
    {'path': 'lib/l10n/app_en.arb', 'added': 120, 'deleted': 0},
    {'path': 'assets/logo.png', 'added': 0, 'deleted': 0},
    {'path': 'lib/merge.dart', 'conflict': true},
    {'path': 'notes.txt', 'untracked': true},
  ],
});

void main() {
  final dir = Platform.environment['POSSE_SHOTS_DIR'] ?? '';

  for (final lang in ['zh', 'en']) {
    Future<void> shot(
      WidgetTester tester,
      String name,
      Widget home, {
      Size size = const Size(390, 844),
      Future<void> Function()? after,
    }) async {
      await tester.binding.setSurfaceSize(size);
      final key = GlobalKey();
      await tester.pumpWidget(
        RepaintBoundary(
          key: key,
          child: MaterialApp(
            debugShowCheckedModeBanner: false,
            locale: Locale(lang),
            localizationsDelegates: AppLocalizations.localizationsDelegates,
            supportedLocales: AppLocalizations.supportedLocales,
            theme: posseTheme(Brightness.light),
            home: home,
          ),
        ),
      );
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 400));
      if (after != null) await after();
      await tester.pump(const Duration(milliseconds: 400));
      if (dir.isEmpty) return;
      await tester.runAsync(() async {
        final boundary =
            key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
        final image = await boundary.toImage(pixelRatio: 2);
        final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
        File('$dir/$name-$lang.png')
            .writeAsBytesSync(bytes!.buffer.asUint8List());
      });
    }

    // 12 小时制另出一张：消息时间写成 3:04 PM、下午3:04
    for (final h24 in [true, false]) {
      testWidgets('chat screenshots ($lang${h24 ? '' : ', 12-hour'})', (
        tester,
      ) async {
        await _fonts(tester);
        tester.platformDispatcher.alwaysUse24HourFormatTestValue = h24;
        tester.binding.handleAccessibilityFeaturesChanged();
        final rpc = _Rpc();
        await shot(
          tester,
          h24 ? 'chat' : 'chat-12h',
          ChatScreen(model: _Model(rpc), tab: _tab),
          after: () async {
            rpc.tabs.add({
              'type': 'snapshot',
              'tabs': [
                {
                  'type': 'terminal',
                  'terminal': 't1',
                  'title': 'Fix login on the phone',
                  'launchAgent': 'claude',
                  'agentStatus': {
                    'state': 'working',
                    'agentType': 'claude',
                    'toolName': 'Bash',
                    'providerSession': {'id': 's1'},
                  },
                },
              ],
            });
            rpc.chat.add({
              'type': 'snapshot',
              'hasMore': true,
              'beforeOffset': 1,
              'messages': [
                _msg('1', 'user', [
                  {'type': 'text', 'text': 'Why does login fail on the phone?'},
                ], 10),
                _msg('2', 'assistant', [
                  {
                    'type': 'tool-call',
                    'name': 'Bash',
                    'callId': 'c1',
                    'input': {'command': 'go test ./...'},
                  },
                ], 20),
                _msg('3', 'assistant', [
                  {
                    'type': 'tool-call',
                    'name': 'Read',
                    'callId': 'c2',
                    'input': {'file_path': 'lib/main.dart'},
                  },
                ], 30),
                _msg('4', 'assistant', [
                  {
                    'type': 'text',
                    'text': 'The token expires before refresh.\n\n```\nflutter test\n```',
                  },
                ], 40),
              ],
            });
            await tester.pump();
            await tester.pump(const Duration(milliseconds: 400));
          },
        );
      });
    }

    testWidgets('chat pieces screenshots ($lang)', (tester) async {
      await _fonts(tester);
      await shot(
        tester,
        'chat-cards',
        Scaffold(
          body: SafeArea(
            child: ListView(
              children: [
                const EarlierRow(loading: false, onLoad: _noop),
                EchoBubble(echo: LocalEcho('Ship it', 2, DateTime(2026))),
                const WorkingRow(tool: 'Bash'),
                Padding(
                  padding: const EdgeInsets.all(12),
                  child: ApprovalCard(
                    approval: const Approval('Bash', 'rm -rf build'),
                    busy: false,
                    onDecision: (_) {},
                  ),
                ),
                Padding(
                  padding: const EdgeInsets.all(12),
                  child: QuestionCard(
                    questions: const [
                      AskQuestion(
                        question: 'Which store first?',
                        options: [AskOption('App Store'), AskOption('Play')],
                      ),
                    ],
                    onSubmit: (_) {},
                  ),
                ),
                ChatComposer(
                  controller: TextEditingController(),
                  images: const [],
                  sending: false,
                  connected: true,
                  ended: true,
                  hint: '',
                  onChanged: (_) {},
                  onSubmit: () {},
                  onPickImage: () {},
                  onRemoveImage: (_) {},
                  onPaste: () {},
                  onContentInserted: (_) {},
                ),
              ],
            ),
          ),
        ),
        size: const Size(390, 1100),
      );
    });

    for (final h24 in [true, false]) {
      testWidgets('tools screenshots ($lang${h24 ? '' : ', 12-hour'})', (
        tester,
      ) async {
        await _fonts(tester);
        tester.platformDispatcher.alwaysUse24HourFormatTestValue = h24;
        tester.binding.handleAccessibilityFeaturesChanged();
        await shot(
          tester,
          h24 ? 'tools' : 'tools-12h',
          Scaffold(
            appBar: AppBar(title: const Text('Tools')),
            body: ToolsView(usage: _usage),
          ),
          size: const Size(390, 1000),
        );
      });
    }

    testWidgets('media screenshots ($lang)', (tester) async {
      await _fonts(tester);
      await shot(
        tester,
        'media',
        MediaScreen(model: _Model(_Rpc()), tab: _tab),
      );
    });

    testWidgets('history screenshots ($lang)', (tester) async {
      await _fonts(tester);
      await shot(tester, 'history', HistoryScreen(model: _Model(_Rpc())));
    });

    testWidgets('git screenshots ($lang)', (tester) async {
      await _fonts(tester);
      await shot(
        tester,
        'git',
        GitScreen(
          title: 'posse',
          loadStatus: () async => _status,
          loadDiff: (file) async => parseGitDiff({
            'path': file.path,
            'diff':
                '@@ -10,3 +10,4 @@ class Chat {\n keep\n-old\n+new\n+more\n',
            'truncated': true,
          }),
        ),
        after: () async {
          await tester.tap(find.text('chat.dart'));
          await tester.pumpAndSettle();
        },
      );
      await shot(
        tester,
        'git-list',
        GitScreen(
          title: 'posse',
          loadStatus: () async => _status,
          loadDiff: (file) async => parseGitDiff({'path': file.path}),
        ),
      );
    });

    testWidgets('files screenshots ($lang)', (tester) async {
      await _fonts(tester);
      await shot(
        tester,
        'files',
        FilesScreen(model: _Model(_Rpc()), worktree: 'w', title: '~/posse'),
        after: () async {
          final context = tester.element(find.byType(FilesPane));
          unawaited(
            showSaveSheet(context, to: SaveTo.album, onSave: (_) async {}),
          );
          await tester.pumpAndSettle();
        },
      );
    });
  }
}

void _noop() {}

var _fontsLoaded = false;

/// 字体每个进程只载一次：重复载入同一字族后，粗体会落到没有汉字的测试字体上。
Future<void> _fonts(WidgetTester tester) async {
  if (_fontsLoaded) return;
  _fontsLoaded = true;
  await loadShotFonts(tester);
}
