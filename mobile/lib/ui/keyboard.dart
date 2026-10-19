import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';

/// 换页时统一收起键盘：返回首页、进下一页都不把上一页的键盘带过去。
///
/// 只看整页切换。底部面板、对话框这类浮层开关时不动，免得在输入框上点个按钮键盘就没了。
/// 终端的输入走 flterm 自己的输入连接，单靠丢焦点收不掉，所以另外让输入法隐藏。
class KeyboardDismisser extends NavigatorObserver {
  @override
  void didPush(Route<dynamic> route, Route<dynamic>? previousRoute) {
    if (route is PageRoute) _dismiss();
  }

  @override
  void didPop(Route<dynamic> route, Route<dynamic>? previousRoute) {
    if (route is PageRoute) _dismiss();
  }

  @override
  void didRemove(Route<dynamic> route, Route<dynamic>? previousRoute) {
    if (route is PageRoute) _dismiss();
  }

  @override
  void didReplace({Route<dynamic>? newRoute, Route<dynamic>? oldRoute}) {
    if (newRoute is PageRoute || oldRoute is PageRoute) _dismiss();
  }

  void _dismiss() {
    FocusManager.instance.primaryFocus?.unfocus();
    SystemChannels.textInput.invokeMethod<void>('TextInput.hide');
  }
}
