import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/protocol/rpc.dart';

/// worktree.ps 每次调用排一个由测试放行的回复。
class _SlowRpc implements PhoneRpc {
  final pending = <Completer<Map<String, Object?>>>[];

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) {
    if (method != 'worktree.ps') return Future.value(const {});
    final c = Completer<Map<String, Object?>>();
    pending.add(c);
    return c.future;
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class _Model extends PosseAppModel {
  _Model(this.fake)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());
  final _SlowRpc fake;
  @override
  PhoneRpc? get rpc => fake;
}

Map<String, Object?> _rows(String name) => {
  'worktrees': [
    {'worktreeId': 'w1', 'displayName': name},
  ],
};

void main() {
  test(
    'an older refresh that answers last does not overwrite a newer one',
    () async {
      final rpc = _SlowRpc();
      final model = _Model(rpc);
      final first = model.refreshWorktrees();
      final second = model.refreshWorktrees();
      expect(rpc.pending, hasLength(2));
      rpc.pending[1].complete(_rows('new'));
      await second;
      rpc.pending[0].complete(_rows('old'));
      await first;
      expect(model.worktrees.single.name, 'new');
    },
  );

  test(
    'the poll skips while a refresh is in flight, an explicit refresh does not',
    () async {
      final rpc = _SlowRpc();
      final model = _Model(rpc);
      final slow = model.refreshWorktrees(poll: true);
      for (var i = 0; i < 5; i++) {
        await model.refreshWorktrees(poll: true);
      }
      expect(rpc.pending, hasLength(1), reason: 'polls piled up');
      // 回到前台（wake）、操作之后的刷新照常发
      final now = model.refreshWorktrees();
      expect(rpc.pending, hasLength(2));
      rpc.pending[1].complete(_rows('now'));
      await now;
      // 卡住的那轮超时失败之后，轮询又能发
      rpc.pending[0].completeError(TimeoutException('worktree.ps'));
      await slow;
      unawaited(model.refreshWorktrees(poll: true));
      expect(rpc.pending, hasLength(3));
      expect(model.worktrees.single.name, 'now');
      expect(
        model.error,
        isNull,
        reason: 'an older failure replaced a newer result',
      );
    },
  );
}
