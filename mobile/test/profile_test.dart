import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/discussions/api.dart';
import 'package:posse_mobile/discussions/models.dart';
import 'package:posse_mobile/discussions/profile.dart';
import 'package:posse_mobile/protocol/rpc.dart';

// 1x1 的 PNG
final png = base64Decode(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
);

class ProfileRpc implements PhoneRpc {
  final calls = <({String method, DiscJson params})>[];
  @override
  Future<DiscJson> call(
    String method,
    DiscJson params, {
    Duration? timeout,
  }) async {
    calls.add((method: method, params: Map.of(params)));
    switch (method) {
      case 'discussion.profile.get':
        return {
          'id': 'self',
          'name': 'Ada',
          'rev': 1,
          'pending': [
            {'room': 'r1', 'title': 'Plan', 'host': 'Bob', 'reason': 'offline'},
          ],
        };
      case 'discussion.profile.set':
        return {'id': 'self', 'name': params['name'] ?? 'Ada', 'rev': 2};
    }
    throw RpcException('method_not_found', '');
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class ProfileModel extends PosseAppModel {
  ProfileModel(this.fake)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());
  final ProfileRpc fake;
  @override
  PhoneRpc? get rpc => fake;
  @override
  String? get activeId => 'test-computer';
}

void main() {
  setUp(DiscAvatars.clear);

  test('people show their latest name; agents and strangers keep theirs', () {
    final collab = {
      'self': 'me',
      'people': [
        {'fp': 'bob', 'name': 'Robert', 'avatar': 'abc'},
        {'fp': 'eve', 'name': ''},
      ],
    };
    expect(
      discAuthorName({
        'kind': 'user',
        'authorId': 'bob',
        'authorName': 'Bob',
      }, collab),
      'Robert',
    );
    expect(
      discAuthorName({
        'kind': 'user',
        'authorId': 'eve',
        'authorName': 'Eve',
      }, collab),
      'Eve',
    );
    expect(
      discAuthorName({
        'kind': 'user',
        'authorId': 'gone',
        'authorName': 'Old',
      }, collab),
      'Old',
    );
    expect(
      discAuthorName({
        'kind': 'reply',
        'authorId': 'bob',
        'authorName': 'Reviewer',
        'authorAgent': 'claude',
      }, collab),
      'Reviewer',
    );
    expect(discAuthorName({'kind': 'user', 'authorName': 'Me'}, {}), 'Me');
    expect(discText(discPerson(collab, 'bob')!, 'avatar'), 'abc');
    expect(discPerson({}, 'bob'), isNull);
  });

  test('avatars are fetched once per sha; failures are retried', () async {
    var count = 0;
    var fail = true;
    Future<DiscJson> call(String method, DiscJson params) async {
      count++;
      expect(method, 'profile.avatar');
      if (fail) throw RpcException('not_found', 'avatar not found');
      return {'sha': params['sha'], 'data': base64Encode(png)};
    }

    expect(await DiscAvatars.get(call, 'a'), isNull);
    fail = false;
    final both = await Future.wait([
      DiscAvatars.get(call, 'a'),
      DiscAvatars.get(call, 'a'),
    ]);
    expect(both.first, png);
    expect(await DiscAvatars.get(call, 'a'), png);
    expect(count, 2);
    expect(DiscAvatars.peek('a'), png);
  });

  testWidgets('avatar falls back to the initial without a usable image', (
    t,
  ) async {
    Future<DiscJson> missing(String method, DiscJson params) async =>
        throw RpcException('not_found', '');
    Future<DiscJson> found(String method, DiscJson params) async => {
      'data': base64Encode(png),
    };
    Widget host(Widget child) => MaterialApp(home: Center(child: child));

    await t.pumpWidget(
      host(const PersonAvatar(call: null, sha: '', name: 'ada', size: 20)),
    );
    expect(find.text('A'), findsOneWidget);

    await t.pumpWidget(
      host(PersonAvatar(call: missing, sha: 'x', name: 'bob', size: 20)),
    );
    await t.pumpAndSettle();
    expect(find.text('B'), findsOneWidget);
    expect(find.byType(Image), findsNothing);

    await t.pumpWidget(
      host(PersonAvatar(call: found, sha: 'y', name: 'bob', size: 20)),
    );
    await t.runAsync(() => Future<void>.delayed(Duration.zero));
    await t.pump();
    expect(find.byType(Image), findsOneWidget);
    expect(find.text('B'), findsNothing);
  });

  testWidgets('profile screen shows pending rooms and saves a trimmed name', (
    t,
  ) async {
    final rpc = ProfileRpc();
    final model = ProfileModel(rpc);
    var saved = false;
    await t.pumpWidget(
      MaterialApp(
        home: Builder(
          builder: (context) => TextButton(
            onPressed: () async =>
                saved = await openProfile(context, DiscussionApi(model)),
            child: const Text('open'),
          ),
        ),
      ),
    );
    await t.tap(find.text('open'));
    await t.pumpAndSettle();
    expect(find.textContaining('Plan'), findsOneWidget);
    expect(find.textContaining('房主上线后自动发送'), findsOneWidget);

    await t.enterText(find.byType(TextField), '   ');
    await t.tap(find.text('保存'));
    await t.pumpAndSettle();
    expect(find.text('名字要 1 到 60 个字'), findsOneWidget);
    expect(
      rpc.calls.where((c) => c.method == 'discussion.profile.set'),
      isEmpty,
    );

    await t.enterText(find.byType(TextField), '  Grace ');
    await t.tap(find.text('保存'));
    await t.pumpAndSettle();
    final set = rpc.calls.singleWhere(
      (c) => c.method == 'discussion.profile.set',
    );
    expect(set.params, {'name': 'Grace'});
    expect(saved, isTrue);
    expect(find.text('open'), findsOneWidget);
    model.dispose();
  });
}
