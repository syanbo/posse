/// 订阅失败的错误码，一句中文。旧常驻进程不认这个方法时也提示更新。
String deskFailText(String code) => switch (code) {
  'unsupported' || 'method_not_found' || 'unknown_method' => '这台电脑的 Hive 需要更新',
  'busy' => '另一台手机正在看',
  'locked' => 'Mac 已锁屏',
  'permission_screen' => '请到 Mac 上允许屏幕录制',
  'unsupported_codec' => '这台电脑不支持手机的视频编码',
  'capture_failed' => '采集失败',
  'lan_unreachable' => '手机和这台 Mac 不在同一个局域网',
  'route_unreachable' => '直连和中转都不通',
  _ => '连接失败',
};

/// 观看结束的 reason，一句中文。
String deskEndText(String reason) => switch (reason) {
  'locked' => 'Mac 已锁屏',
  'stopped' => '已在 Mac 上停止',
  'unsubscribed' => '已停止观看',
  'no_report' => '电脑没有收到画面报告',
  'capture_failed' => '采集失败',
  _ => '连接已结束',
};

const deskAccessText = '能看不能操作，请到 Mac 上打开辅助功能';

const deskIndirectText = '画面过不了隧道，请在同一个 Wi-Fi 下扫这台 Mac 的局域网码，或改用 Tailscale';
