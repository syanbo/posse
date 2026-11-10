import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';

void main() {
  testWidgets('切到后台才算 away，控制中心那样只到 inactive 不算', (tester) async {
    final binding = tester.binding;
    binding.handleAppLifecycleStateChanged(AppLifecycleState.resumed);
    expect(appAway, isFalse);
    binding.handleAppLifecycleStateChanged(AppLifecycleState.inactive);
    expect(appAway, isFalse);
    binding.handleAppLifecycleStateChanged(AppLifecycleState.hidden);
    expect(appAway, isTrue);
    binding.handleAppLifecycleStateChanged(AppLifecycleState.paused);
    expect(appAway, isTrue);
    binding.handleAppLifecycleStateChanged(AppLifecycleState.hidden);
    binding.handleAppLifecycleStateChanged(AppLifecycleState.inactive);
    binding.handleAppLifecycleStateChanged(AppLifecycleState.resumed);
    expect(appAway, isFalse);
  });
}
