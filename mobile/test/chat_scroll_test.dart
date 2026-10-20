import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/app_state.dart';
import 'package:hive_mobile/chat/chat.dart';
import 'package:hive_mobile/protocol/rpc.dart';
import 'package:hive_mobile/workspace/models.dart';

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

class FakeModel extends HiveAppModel {
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
