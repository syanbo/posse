import 'dart:async';

import 'package:fake_async/fake_async.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/chat/latency_label.dart';
import 'package:posse_mobile/protocol/latency.dart';
import 'package:posse_mobile/protocol/rpc.dart';

/// 只认 ping 的连接：[reply] 决定每次怎么回。
class _PingRpc implements PhoneRpc {
  _PingRpc(this.reply);

  Future<void> Function(_PingRpc rpc, String method) reply;
  final methods = <String>[];
  var dropped = false;

  @override
  var receivedBytes = 0;
  @override
  var sentBytes = 0;
  @override
  var repliesReceived = 0;

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    methods.add(method);
    await reply(this, method);
    return {};
  }

  @override
  void drop() => dropped = true;

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

LatencyMeter _meter(_PingRpc rpc) => LatencyMeter(rpc, onChange: () {});

void main() {
  test('shows the median of the last three round trips', () async {
    final delays = [0, 60, 300, 40];
    var i = 0;
    final rpc = _PingRpc(
      (_, _) => Future.delayed(Duration(milliseconds: delays[i++])),
    );
    final meter = _meter(rpc);
    for (var n = 0; n < delays.length; n++) {
      await meter.tick();
    }
    // 最近三次是 60、300、40：中位数 60，一次 300 的抖动不算
    final ms = meter.latency!.inMilliseconds;
    expect(ms, inInclusiveRange(55, 200));
    expect(rpc.methods.toSet(), {'runtime.ping'});
  });

  test('a ping stuck behind a large reply is not a sample', () async {
    final rpc = _PingRpc((rpc, _) async => rpc.receivedBytes += 1 << 20);
    final meter = _meter(rpc);
    await meter.tick();
    expect(meter.latency, isNull);
    expect(meter.unresponsive, isFalse);
  });

  test(
    'an old desktop without runtime.ping is timed with status.get',
    () async {
      final rpc = _PingRpc((_, method) async {
        if (method == 'runtime.ping') {
          throw RpcException('method_not_found', method);
        }
      });
      final meter = _meter(rpc);
      await meter.tick();
      await meter.tick();
      expect(rpc.methods, ['runtime.ping', 'status.get']);
      expect(meter.latency, isNotNull);
    },
  );

  test('three silent timeouts drop the connection', () async {
    final rpc = _PingRpc((_, method) async => throw TimeoutException(method));
    final meter = _meter(rpc);
    await meter.tick();
    expect(meter.unresponsive, isTrue);
    expect(rpc.dropped, isFalse);
    await meter.tick();
    await meter.tick();
    expect(rpc.dropped, isTrue);
  });

  test('a silent desktop is dropped about three timeouts later', () {
    fakeAsync((async) {
      // 像真的连接一样：等满超时才抛
      final rpc = _PingRpc((_, method) async {
        await Future<void>.delayed(const Duration(seconds: 5));
        throw TimeoutException(method);
      });
      LatencyMeter(rpc, onChange: () {}).start();
      async.elapse(const Duration(seconds: 14));
      expect(rpc.dropped, isFalse);
      // 第一次 ping 一发出就没回：5、10、15 秒各超时一次，不在两次之间再空等一个间隔
      async.elapse(const Duration(seconds: 2));
      expect(rpc.dropped, isTrue);
      expect(rpc.methods.length, 3);
    });
  });

  test('a send that fails at once after a timeout waits for the next tick', () {
    fakeAsync((async) {
      // 后台久了回来：ping 先超时一次，之后连接已断、发送立即失败，断线通知还在事件队列里
      var first = true;
      final rpc = _PingRpc((_, method) async {
        if (first) {
          first = false;
          await Future<void>.delayed(const Duration(seconds: 5));
          throw TimeoutException(method);
        }
        throw StateError('closed');
      });
      LatencyMeter(rpc, onChange: () {}).start();
      async.elapse(const Duration(seconds: 5));
      // 5 秒时超时补测一次、定时器也到点测一次；立即失败的不再接着补测，不然只走微任务、永远停不下来
      expect(rpc.methods.length, 3);
      async.elapse(const Duration(seconds: 5));
      expect(rpc.methods.length, 4);
    });
  });

  test(
    'timeouts while other replies keep arriving are not a dead link',
    () async {
      // 慢线路上分段下大文件：ping 排在后面超时了，但视频分段的回复一直在回来
      final rpc = _PingRpc((rpc, method) async {
        rpc.receivedBytes += 1 << 20;
        rpc.repliesReceived++;
        throw TimeoutException(method);
      });
      final meter = _meter(rpc);
      for (var n = 0; n < 5; n++) {
        await meter.tick();
      }
      expect(meter.unresponsive, isFalse);
      expect(rpc.dropped, isFalse);
    },
  );

  test(
    'pushes alone do not keep a connection that stopped answering',
    () async {
      // 电脑那边处理请求卡住了，终端输出、标签推送照样在来：所有请求超时，要重连
      final rpc = _PingRpc((rpc, method) async {
        rpc.receivedBytes += 4 << 10;
        throw TimeoutException(method);
      });
      final meter = _meter(rpc);
      for (var n = 0; n < 3; n++) {
        await meter.tick();
      }
      expect(rpc.dropped, isTrue);
    },
  );

  testWidgets(
    'latency label: bars and bare number, green when fast, red when silent',
    (tester) async {
      Future<Icon> show(Duration? latency, {bool unresponsive = false}) async {
        await tester.pumpWidget(
          MaterialApp(
            theme: posseTheme(Brightness.dark),
            home: Scaffold(
              appBar: AppBar(
                title: LatencyLabel(
                  latency: latency,
                  unresponsive: unresponsive,
                ),
              ),
            ),
          ),
        );
        return tester.widget<Icon>(find.byType(Icon).last);
      }

      // 还没测到：不占位置
      await tester.pumpWidget(
        const MaterialApp(home: Scaffold(body: LatencyLabel())),
      );
      expect(find.byType(Icon), findsNothing);
      final fast = await show(const Duration(milliseconds: 42));
      // 写数值不写单位，读屏念全
      expect(find.text('42'), findsOneWidget);
      expect(find.bySemanticsLabel('和电脑的延迟 42 毫秒'), findsOneWidget);
      final c = PosseColors.of(tester.element(find.byType(LatencyLabel)));
      expect(fast.color, c.sage);
      final mid = await show(const Duration(milliseconds: 200));
      expect(mid.color, c.muted);
      final slow = await show(const Duration(milliseconds: 420));
      expect(slow.color, c.honey);
      final silent = await show(null, unresponsive: true);
      expect(silent.color, c.danger);
      final text = tester.widget<Text>(find.byType(Text).last);
      expect(text.data, '无响应');
      // 在顶栏里也不跟着标题变粗
      expect(text.style!.fontWeight, FontWeight.w400);
    },
  );

  test('signal bars drop as latency grows', () {
    IconData at(int ms) => latencyIcon(Duration(milliseconds: ms));
    expect(at(40), Icons.signal_cellular_alt_rounded);
    expect(at(149), Icons.signal_cellular_alt_rounded);
    expect(at(150), Icons.signal_cellular_alt_2_bar_rounded);
    expect(at(299), Icons.signal_cellular_alt_2_bar_rounded);
    expect(at(300), Icons.signal_cellular_alt_1_bar_rounded);
    expect(
      latencyIcon(null, unresponsive: true),
      Icons.signal_cellular_connected_no_internet_0_bar_rounded,
    );
  });
}
