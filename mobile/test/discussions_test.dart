import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/discussions/models.dart';
import 'package:posse_mobile/discussions/api.dart';
import 'package:shared_preferences/shared_preferences.dart';

DiscJson message(int n, {bool key = false}) => {
  'id': 'm$n',
  'seq': n,
  'body': 'Message $n',
  if (key) 'keyAt': 'now',
};
DiscJson page(
  int rev,
  List<DiscJson> messages, {
  bool delta = false,
  bool reset = false,
  bool more = false,
  bool after = false,
}) => {
  'discussion': {'key': 'room', 'rev': rev, 'msgSeq': rev},
  'messages': messages,
  'members': <DiscJson>[],
  'executions': <DiscJson>[],
  'delta': delta,
  'reset': reset,
  'more': more,
  'moreAfter': after,
};

void main() {
  test('late poll cannot overwrite a navigation page', () async {
    final stale = Completer<DiscJson>();
    var count = 0;
    final state = DiscussionState((method, p) async {
      count++;
      if (count == 1) return page(3, [message(2), message(3)]);
      if (count == 2) return stale.future;
      return page(4, [message(1)], after: true);
    }, 'room');
    await state.page();
    final polling = state.poll();
    await state.page(message: 'm1');
    stale.complete(page(4, [message(4)], delta: true));
    await polling;
    expect(state.messages.map((m) => m['id']), ['m1']);
    expect(state.moreAfter, true);
    state.dispose();
  });
  test(
    'reading history merges key edits without appending new messages',
    () async {
      var count = 0;
      final state = DiscussionState((method, p) async {
        if (count++ == 0) return page(5, [message(1), message(2)], after: true);
        return page(6, [message(2, key: true), message(6)], delta: true);
      }, 'room');
      await state.page();
      await state.poll();
      expect(state.messages.map((m) => m['seq']), [1, 2]);
      expect(state.keys.single['id'], 'm2');
      expect(state.rev, 6);
      state.dispose();
    },
  );
  test(
    'large delta resets latest page and preserves old history window',
    () async {
      for (final old in [false, true]) {
        var count = 0;
        final state = DiscussionState(
          (method, p) async => count++ == 0
              ? page(5, [message(1)], after: old)
              : page(
                  200,
                  [message(199), message(200)],
                  reset: true,
                  more: true,
                ),
          'room',
        );
        await state.page();
        await state.poll();
        expect(state.messages.first['seq'], old ? 1 : 199);
        expect(state.moreAfter, old);
        state.dispose();
      }
    },
  );
  test(
    'message window retains at most 500 and pagination flags follow trim',
    () async {
      var count = 0;
      final state = DiscussionState((method, p) async {
        if (count++ == 0) {
          return page(
            600,
            List.generate(500, (i) => message(i + 101)),
            more: true,
          );
        }
        return page(600, List.generate(100, (i) => message(i + 1)));
      }, 'room');
      await state.page();
      await state.page(before: 101);
      expect(state.messages.length, 500);
      expect(state.first, 1);
      expect(state.last, 500);
      expect(state.moreAfter, true);
      state.dispose();
    },
  );
  test('drafts and read cursors are isolated by computer and room', () async {
    SharedPreferences.setMockInitialValues({});
    final a = DiscussionStorage('a', 'room'),
        b = DiscussionStorage('b', 'room');
    await a.write({
      'body': 'draft',
      'clientId': 'same-retry',
      'read': 8,
      'pending': {'clientId': 'same-retry'},
    });
    expect((await a.read())['clientId'], 'same-retry');
    expect(await b.read(), isEmpty);
  });
  test(
    'paging does not skip member changes by advancing the delta cursor',
    () async {
      var count = 0;
      final state = DiscussionState((method, p) async {
        count++;
        if (count == 1) return page(5, [message(5)], more: true);
        if (count == 2) return page(6, [message(4)]);
        expect(p['since'], 5);
        return {
          ...page(6, [], delta: true),
          'members': [
            {'id': 'member', 'name': 'renamed'},
          ],
        };
      }, 'room');
      await state.page();
      await state.page(before: 5);
      await state.poll();
      expect(state.members.single['name'], 'renamed');
      state.dispose();
    },
  );
  test('old message edits cannot break the latest contiguous window', () async {
    var calls = 0;
    final state = DiscussionState(
      (method, p) async => calls++ == 0
          ? page(500, List.generate(51, (i) => message(i + 450)), more: true)
          : page(501, [message(3, key: true), message(501)], delta: true),
      'room',
    );
    await state.page();
    await state.poll();
    expect(state.first, 450);
    expect(state.last, 501);
    expect(state.messages.length, 52);
    expect(state.keys.single['seq'], 3);
    state.dispose();
  });
  test('reset while reading history reloads its execution cards', () async {
    var calls = 0;
    final state = DiscussionState((method, p) async {
      calls++;
      if (calls == 1) {
        return {
          ...page(5, [message(1)], after: true),
          'executions': [
            {'id': 'x', 'message': 'm1', 'state': 'running'},
          ],
        };
      }
      if (calls == 2) return page(200, [message(200)], reset: true);
      expect(p['before'], 2);
      return {
        ...page(200, [message(1)], after: true),
        'executions': [
          {'id': 'x', 'message': 'm1', 'state': 'done'},
        ],
      };
    }, 'room');
    await state.page();
    await state.poll();
    expect(state.messages.single['id'], 'm1');
    expect(state.executions.single['state'], 'done');
    state.dispose();
  });
  test('old owner records belong to the host user', () {
    expect(discMine({}, 'self'), true);
    expect(discMine({'owner': 'other'}, 'self'), false);
  });
}
