import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:intl/date_symbol_data_local.dart';

// App 里 intl 的日期数据由 flutter_localizations 载入译文表时登记；测试里自己建的 MaterialApp
// 不带译文表，单元测试也不建界面，这里先登记好。
// 测试环境的 alwaysUse24HourFormat 默认是 false：每个测试前固定成 24 小时制，钟点和原来一样；
// 要看 12 小时制的测试自己改 alwaysUse24HourFormatTestValue。
Future<void> testExecutable(FutureOr<void> Function() testMain) async {
  await initializeDateFormatting();
  setUp(() {
    TestWidgetsFlutterBinding.ensureInitialized()
            .platformDispatcher
            .alwaysUse24HourFormatTestValue =
        true;
  });
  await testMain();
}
