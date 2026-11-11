import 'dart:convert';

import 'package:fake_async/fake_async.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/protocol/rpc.dart';

/// worktree.ps 回一个工作区，图标是 [icon] 这张图；posse.projectIcon 在 [fail] 为真时报错，记下每次要的名字。
class _Rpc implements PhoneRpc {
  String icon = 'a.png';
  bool fail = true;
  final asked = <String>[];

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    if (method == 'posse.projectIcon') {
      asked.add(params['name']! as String);
      if (fail) throw StateError('temporary icon failure');
      return {
        'data': base64Encode([1, 2, 3]),
      };
    }
    if (method == 'worktree.ps') {
      return {
        'worktrees': [
          {
            'worktreeId': 'w',
            'displayName': 'fixture',
            'repoIcon': {'image': icon},
          },
        ],
      };
    }
    return const {};
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class _Model extends PosseAppModel {
  _Model(this.fake)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());
  _Rpc? fake;
  @override
  PhoneRpc? get rpc => fake;
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  // 和模型里的重试间隔一样
  const after = Duration(seconds: 30);
  const tick = Duration(seconds: 1);

  // 只在首页建的时候要过一次，之后没有任何通知、重建：失败了到时候自己再取，取到了通知一次就停。
  test('a failed project icon is fetched again on its own', () {
    fakeAsync((async) {
      final rpc = _Rpc();
      final model = _Model(rpc);
      model.refreshWorktrees();
      async.flushMicrotasks();
      var notified = 0;
      model.addListener(() => notified++);

      expect(model.projectIcon('a.png'), isNull);
      async.flushMicrotasks();
      expect(rpc.asked, ['a.png']);
      async.elapse(after - tick);
      expect(rpc.asked, hasLength(1));
      async.elapse(tick * 2);
      expect(rpc.asked, hasLength(2));
      // 接着失败就接着隔一阵再取，失败不通知
      async.elapse(after);
      expect(rpc.asked, hasLength(3));
      expect(notified, 0);

      rpc.fail = false;
      async.elapse(after);
      expect(rpc.asked, hasLength(4));
      expect(notified, 1);
      expect(model.projectIcon('a.png'), [1, 2, 3]);
      async.elapse(after * 4);
      expect(rpc.asked, hasLength(4));
      model.dispose();
    });
  });

  // 电脑换了图标：新的照常取，旧的那张不再重试。
  test('a changed icon is fetched and the old one is no longer retried', () {
    fakeAsync((async) {
      final rpc = _Rpc();
      final model = _Model(rpc);
      model.refreshWorktrees();
      async.flushMicrotasks();
      model.projectIcon('a.png');
      async.flushMicrotasks();

      rpc.icon = 'b.png';
      var notified = 0;
      model.addListener(() => notified++);
      model.refreshWorktrees(poll: true);
      async.flushMicrotasks();
      expect(notified, 1);
      // 首页跟着重建，要新的这张
      expect(model.projectIcon('b.png'), isNull);
      async.flushMicrotasks();
      expect(rpc.asked, ['a.png', 'b.png']);

      async.elapse(after + tick);
      // 只重试还在用的 b.png
      expect(rpc.asked, ['a.png', 'b.png', 'b.png']);
      model.dispose();
    });
  });

  // 到点时没连着：不取；重连后首页重建时照常取。关掉模型后不再取。
  test('no retry while offline or after dispose', () {
    fakeAsync((async) {
      final rpc = _Rpc();
      final model = _Model(rpc);
      model.refreshWorktrees();
      async.flushMicrotasks();
      model.projectIcon('a.png');
      async.flushMicrotasks();

      model.fake = null;
      async.elapse(after + tick);
      expect(rpc.asked, hasLength(1));
      model.fake = rpc;
      model.projectIcon('a.png');
      async.flushMicrotasks();
      expect(rpc.asked, hasLength(2));

      model.dispose();
      async.elapse(after * 3);
      expect(rpc.asked, hasLength(2));
    });
  });
}
