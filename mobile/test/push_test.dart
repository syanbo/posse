import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/push/push.dart';

void main() {
  // main.dart 在 runApp 之前建模型，那时还没有 binding：这里在 setUp 初始化 binding 之前建
  Object? early;
  try {
    ChannelPush();
  } catch (e) {
    early = e;
  }
  test('ChannelPush can be built before the binding exists', () {
    expect(early, isNull);
  });

  test('push switches default on and round trip', () {
    final s = AppSettings();
    expect(s.pushWaiting && s.pushDone, isTrue);
    expect(s.pushAsked, isFalse);
    s
      ..pushWaiting = false
      ..pushAsked = true;
    final back = AppSettings.decode(jsonEncode(s.toJson()));
    expect(back.pushWaiting, isFalse);
    expect(back.pushDone, isTrue);
    expect(back.pushAsked, isTrue);
  });

  test('no push platform in tests means unsupported', () {
    final model = PosseAppModel(store: MemoryPairingStore());
    // 测试环境不是 iOS，不碰原生通道
    expect(model.push, isNull);
    expect(model.pushStatus, PushStatus.unsupported);
  });
}
