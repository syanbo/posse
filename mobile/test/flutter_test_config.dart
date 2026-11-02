import 'dart:async';

import 'package:intl/date_symbol_data_local.dart';

// App 里 intl 的日期数据由 flutter_localizations 载入译文表时登记；测试里自己建的 MaterialApp
// 不带译文表，单元测试也不建界面，这里先登记好。
Future<void> testExecutable(FutureOr<void> Function() testMain) async {
  await initializeDateFormatting();
  await testMain();
}
