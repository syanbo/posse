import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/chat/chat.dart';
import 'package:posse_mobile/chat/draft.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/workspace/models.dart';

/// 只实现对话页用到的那几条 RPC：订阅由测试往里推帧。
class FakeRpc implements PhoneRpc {
  final chat = StreamController<Map<String, Object?>>();
  final tabs = StreamController<Map<String, Object?>>();
  final calls = <String>[];
  Map<String, Object?> Function(Map<String, Object?> params)? readSession;

  @override
  Future<RpcStream> subscribe(
    String method,
    Map<String, Object?> params,
  ) async {
    final controller = method == 'nativeChat.subscribe' ? chat : tabs;
    return RpcStream(method, controller.stream, () async {});
  }

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    calls.add(method);
    if (method == 'terminal.send') {
      return {
        'send': {
          'accepted': true,
          'writeSettlement': {'outcome': 'accepted'},
        },
      };
    }
    if (method == 'nativeChat.readSession' && readSession != null) {
      return readSession!(params);
    }
    return {};
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class FakeModel extends PosseAppModel {
  FakeModel(this.fake)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());

  final FakeRpc fake;

  @override
  PhoneRpc? get rpc => fake;
}

const tab = SessionTab(
  worktreeId: 'w',
  title: 'claude',
  terminalId: 't1',
  agent: 'claude',
  state: 'idle',
  sessionId: 's1',
);

Map<String, Object?> tabsFrame(String state) => {
  'type': 'snapshot',
  'tabs': [
    {
      'type': 'terminal',
      'terminal': 't1',
      'title': 'claude',
      'launchAgent': 'claude',
      'agentStatus': {
        'state': state,
        'agentType': 'claude',
        'providerSession': {'id': 's1'},
      },
    },
  ],
};

Map<String, Object?> text(String id, String role, String body, int offset) => {
  'id': id,
  'role': role,
  'source': 'transcript',
  'transcriptOffset': offset,
  'blocks': [
    {'type': 'text', 'text': body},
  ],
};

Map<String, Object?> tool(String id, String call, int offset) => {
  'id': id,
  'role': 'assistant',
  'source': 'transcript',
  'transcriptOffset': offset,
  'blocks': [
    {
      'type': 'tool-call',
      'name': 'Bash',
      'callId': call,
      'input': {'command': 'sleep 8; echo $call'},
    },
  ],
};

String para(int n) =>
    List.generate(6, (i) => '第 $n 段的第 $i 行，用来把一条消息撑高一些。').join('\n');

Future<FakeRpc> pumpChat(
  WidgetTester tester,
  List<Map<String, Object?>> messages, {
  bool hasMore = false,
  int beforeOffset = 0,
}) async {
  tester.view.physicalSize = const Size(1080, 2340);
  tester.view.devicePixelRatio = 3;
  addTearDown(tester.view.reset);
  final fake = FakeRpc();
  final model = FakeModel(fake);
  await tester.pumpWidget(
    MaterialApp(
      home: ChatScreen(model: model, tab: tab),
    ),
  );
  await tester.pump();
  fake.tabs.add(tabsFrame('idle'));
  fake.chat.add({
    'type': 'snapshot',
    'messages': messages,
    'hasMore': hasMore,
    'beforeOffset': beforeOffset,
  });
  await tester.pumpAndSettle();
  return fake;
}

ScrollPosition listPosition(WidgetTester tester) {
  final scrollable = tester.state<ScrollableState>(
    find
        .descendant(
          of: find.byType(ListView),
          matching: find.byType(Scrollable),
        )
        .first,
  );
  return scrollable.position;
}

void main() {
  testWidgets('停在中间发送后贴到底部，之后的回复不把它拉回去', (tester) async {
    final messages = <Map<String, Object?>>[
      for (var i = 0; i < 30; i++)
        i.isEven
            ? text('m$i', 'user', para(i), i * 10)
            : text('m$i', 'assistant', para(i), i * 10),
    ];
    final fake = await pumpChat(tester, messages);
    final position = listPosition(tester);
    expect(position.pixels, 0);

    // 往上翻离开底部，底部来新内容，出现「新消息」
    await tester.drag(find.byType(ListView), const Offset(0, 1500));
    await tester.pumpAndSettle();
    expect(position.pixels, greaterThan(500));
    fake.tabs.add(tabsFrame('working'));
    fake.chat.add({
      'type': 'appended',
      'messages': [tool('t1', 'c1', 400)],
    });
    await tester.pumpAndSettle();
    expect(find.text('新消息'), findsOneWidget);

    await tester.enterText(find.byType(TextField), '只读，回复 收到');
    await tester.pump();
    await tester.tap(find.byTooltip('发送'));
    await tester.pump();
    await tester.pump();
    expect(position.pixels, 0, reason: '发送后马上贴底');

    // 服务端陆续推回：用户消息顶掉回显、状态行、回复
    fake.chat.add({
      'type': 'appended',
      'messages': [text('u2', 'user', '只读，回复 收到', 410)],
    });
    await tester.pump(const Duration(milliseconds: 50));
    fake.chat.add({'type': 'status', 'text': 'Percolating…'});
    await tester.pump(const Duration(milliseconds: 50));
    fake.chat.add({
      'type': 'appended',
      'messages': [text('a2', 'assistant', '收到。\n${para(99)}', 420)],
    });
    await tester.pump(const Duration(milliseconds: 50));
    fake.chat.add({'type': 'status', 'text': ''});
    fake.tabs.add(tabsFrame('idle'));
    await tester.pumpAndSettle();
    expect(position.pixels, 0, reason: '回复到了仍然贴底');
    expect(find.text('新消息'), findsNothing);
  });

  testWidgets('停在底部时草稿跟着长，正式消息到了撤掉', (tester) async {
    final messages = <Map<String, Object?>>[
      for (var i = 0; i < 12; i++)
        text('m$i', i.isEven ? 'user' : 'assistant', para(i), i * 10),
    ];
    final fake = await pumpChat(tester, messages);
    final position = listPosition(tester);
    fake.tabs.add(tabsFrame('working'));
    fake.chat.add({'type': 'draft', 'text': '草稿开头'});
    await tester.pumpAndSettle();
    expect(find.text('正在输出'), findsOneWidget);
    expect(find.text('草稿开头'), findsOneWidget);

    // 越写越长，一直贴着底部，最后一行看得见
    final body = StringBuffer('草稿开头');
    for (var i = 0; i < 6; i++) {
      body.write('\n\n${para(100 + i)}');
      fake.chat.add({'type': 'draft', 'text': body.toString()});
      await tester.pumpAndSettle();
      expect(position.pixels, 0);
    }
    final listBottom = tester.getBottomLeft(find.byType(ListView)).dy;
    final last = find.textContaining('第 105 段的第 5 行');
    expect(last, findsOneWidget);
    expect(tester.getBottomLeft(last).dy, lessThanOrEqualTo(listBottom));
    expect(find.text('新消息'), findsNothing);

    // 代码和框线表格用等宽字体
    fake.chat.add({
      'type': 'draft',
      'text': '${body.toString()}\n\n  if err != nil {\n│ a │ b │',
    });
    await tester.pumpAndSettle();
    final code = tester.widget<Text>(find.textContaining('if err != nil'));
    expect(code.style?.fontFamily, monoFamily);

    // 会话文件里的正式回复到了，草稿撤掉
    fake.chat.add({
      'type': 'appended',
      'messages': [text('a1', 'assistant', '正式的回复。', 500)],
    });
    await tester.pumpAndSettle();
    expect(find.text('正在输出'), findsNothing);
    expect(find.text('正式的回复。'), findsOneWidget);
    expect(position.pixels, 0);

    // 电脑推空串也撤掉
    fake.chat.add({'type': 'draft', 'text': '下一段'});
    await tester.pumpAndSettle();
    expect(find.text('正在输出'), findsOneWidget);
    fake.chat.add({'type': 'draft', 'text': ''});
    await tester.pumpAndSettle();
    expect(find.text('正在输出'), findsNothing);
  });

  test('草稿按段拆开，代码和框线表格单独成等宽段', () {
    final parts = draftParts('第一段\n接着\n\n第二段\n\n  if x {\n\n  }\n│ a │\n尾巴');
    expect(parts.map((p) => (p.mono, p.text)).toList(), [
      (false, '第一段\n接着'),
      (false, '第二段'),
      (true, '  if x {\n\n  }\n│ a │'),
      (false, '尾巴'),
    ]);
  });

  testWidgets('往上翻着看时草稿变长画面不跳', (tester) async {
    final messages = <Map<String, Object?>>[
      for (var i = 0; i < 30; i++)
        text('m$i', i.isEven ? 'user' : 'assistant', para(i), i * 10),
    ];
    final fake = await pumpChat(tester, messages);
    fake.tabs.add(tabsFrame('working'));
    fake.chat.add({'type': 'draft', 'text': para(200)});
    await tester.pumpAndSettle();
    await tester.drag(find.byType(ListView), const Offset(0, 1500));
    await tester.pumpAndSettle();
    final position = listPosition(tester);
    expect(position.pixels, greaterThan(500));

    // 记下正在看的那一行
    var seen = find.byKey(const Key('none'));
    for (var i = 29; i >= 0; i--) {
      final f = find
          .textContaining('第 $i 段的第 3 行', findRichText: true)
          .hitTestable();
      if (f.evaluate().isNotEmpty) {
        // 之后不再要求点得到：「新消息」浮在列表上，可能盖住它
        seen = find.textContaining('第 $i 段的第 3 行', findRichText: true).first;
        break;
      }
    }
    expect(seen, findsOneWidget);
    final top = tester.getTopLeft(seen).dy;
    final body = StringBuffer(para(200));
    for (var i = 0; i < 4; i++) {
      body.write('\n\n${para(300 + i)}');
      fake.chat.add({'type': 'draft', 'text': body.toString()});
      await tester.pumpAndSettle();
      expect(tester.getTopLeft(seen).dy, closeTo(top, 1));
    }
    expect(find.text('新消息'), findsOneWidget);

    // 正式消息换掉草稿时也不跳
    fake.chat.add({
      'type': 'appended',
      'messages': [text('a1', 'assistant', body.toString(), 500)],
    });
    await tester.pumpAndSettle();
    expect(tester.getTopLeft(seen).dy, closeTo(top, 1));
  });

  testWidgets('一直往上翻能看到最早那条消息的开头', (tester) async {
    final messages = <Map<String, Object?>>[
      text('first', 'user', '最开始的提示。${para(0)}', 0),
      for (var i = 1; i < 40; i++)
        i % 3 == 0
            ? text('m$i', 'assistant', para(i), i * 10)
            : tool('t$i', 'c$i', i * 10),
    ];
    await pumpChat(tester, messages);
    for (var i = 0; i < 12; i++) {
      await tester.fling(find.byType(ListView), const Offset(0, 1200), 3000);
      await tester.pumpAndSettle();
    }
    final position = listPosition(tester);
    expect(position.pixels, position.maxScrollExtent);
    final head = find.textContaining('最开始的提示', findRichText: true);
    expect(head, findsWidgets);
    final listTop = tester.getTopLeft(find.byType(ListView)).dy;
    expect(tester.getTopLeft(head.first).dy, greaterThanOrEqualTo(listTop));
  });

  testWidgets('往上翻读进更早的一页后，最早那条的开头也看得见', (tester) async {
    Map<String, Object?> row(int i) => i == 0
        ? text('first', 'user', '最开始的提示。${para(0)}', 0)
        : i % 3 == 0
        ? text('m$i', 'assistant', para(i), i * 10)
        : tool('t$i', 'c$i', i * 10);
    final fake = await pumpChat(
      tester,
      [for (var i = 20; i < 40; i++) row(i)],
      hasMore: true,
      beforeOffset: 200,
    );
    fake.readSession = (params) => {
      'messages': [for (var i = 0; i < 20; i++) row(i)],
      'hasMore': false,
      'beforeOffset': 0,
    };
    for (var i = 0; i < 16; i++) {
      await tester.fling(find.byType(ListView), const Offset(0, 1200), 3000);
      await tester.pumpAndSettle();
    }
    expect(fake.calls, contains('nativeChat.readSession'));
    final position = listPosition(tester);
    expect(position.pixels, position.maxScrollExtent);
    final head = find.textContaining('最开始的提示', findRichText: true);
    expect(head, findsWidgets);
    final listTop = tester.getTopLeft(find.byType(ListView)).dy;
    expect(tester.getTopLeft(head.first).dy, greaterThanOrEqualTo(listTop));
  });
}
