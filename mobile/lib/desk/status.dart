import '../l10n/l10n.dart';

/// 订阅失败的错误码，一句话。旧常驻进程不认这个方法时也提示更新。
/// no_plugin、disconnected 是手机自己的：没有桌面插件、和电脑的连接断了。
String deskFailText(AppLocalizations l, String code) => switch (code) {
  'unsupported' || 'method_not_found' || 'unknown_method' => l.deskFailOutdated,
  'busy' => l.deskFailBusy,
  'locked' => l.deskMacLocked,
  'permission_screen' => l.deskFailScreenPermission,
  'unsupported_codec' => l.deskFailCodec,
  'capture_failed' => l.deskCaptureFailed,
  'lan_unreachable' => l.deskFailLan,
  'route_unreachable' => l.deskFailRoute,
  'no_plugin' => l.deskFailNoPlugin,
  'disconnected' => l.deskFailDisconnected,
  _ => l.deskFailGeneric,
};

/// 观看结束的 reason，一句话。
String deskEndText(AppLocalizations l, String reason) => switch (reason) {
  'locked' => l.deskMacLocked,
  'stopped' => l.deskEndStopped,
  'unsubscribed' => l.deskEndUnsubscribed,
  'no_report' => l.deskEndNoReport,
  'capture_failed' => l.deskCaptureFailed,
  _ => l.deskEndGeneric,
};
