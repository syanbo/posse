// ignore: unused_import
import 'package:intl/intl.dart' as intl;

import 'app_localizations.dart';

// ignore_for_file: type=lint

/// The translations for Chinese (`zh`).
class AppLocalizationsZh extends AppLocalizations {
  AppLocalizationsZh([String locale = 'zh']) : super(locale);

  @override
  String get commonRefresh => '刷新';

  @override
  String get commonResetToDefault => '恢复默认';

  @override
  String get commonRetry => '重试';

  @override
  String get deskAddressRequired => '先填地址';

  @override
  String get deskAlreadyUnlocked => 'Mac 已经解锁，密码没有发送';

  @override
  String get deskBack => '返回';

  @override
  String get deskCaptureFailed => '采集失败';

  @override
  String get deskChangePasswordTip => '换密码或忘掉';

  @override
  String get deskConnecting => '正在连接';

  @override
  String get deskConnectionNote =>
      '默认走订阅。画面发到现在连着的电脑，端口由电脑分配。这里改的是 UDP 地址，留着调试。';

  @override
  String get deskConnectionSettings => '连接设置';

  @override
  String get deskDebug => '调试';

  @override
  String deskDebugDecode(String ms) {
    return '解码 $ms ms';
  }

  @override
  String deskDebugHeadline(String fps, String received) {
    return '$fps  收到 $received fps';
  }

  @override
  String deskDebugInput(String sent, String acked) {
    return '输入：已发 $sent、已确认 $acked';
  }

  @override
  String deskDebugKeyframes(String count) {
    return '关键帧 $count';
  }

  @override
  String deskDebugLoss(String loss) {
    return '丢包 $loss';
  }

  @override
  String deskDebugPath(String path) {
    return '线路 $path';
  }

  @override
  String deskDebugRate(String bitrate, String bytes, String peak) {
    return '码率 $bitrate（$bytes）  峰值 $peak';
  }

  @override
  String deskDebugView(String zoom, String x, String y) {
    return '缩放 $zoom  平移 $x, $y';
  }

  @override
  String get deskDisconnect => '断开';

  @override
  String get deskDisconnected => '已断开';

  @override
  String deskDisplayNumbered(String number) {
    return '显示器 $number';
  }

  @override
  String get deskEndGeneric => '连接已结束';

  @override
  String get deskEndNoReport => '电脑没有收到画面报告';

  @override
  String get deskEndStopped => '已在 Mac 上停止';

  @override
  String get deskEndUnsubscribed => '已停止观看';

  @override
  String get deskEnterPassword => '输入密码';

  @override
  String get deskEntireScreen => '整块屏';

  @override
  String deskEventClick(String point) {
    return '单击 $point';
  }

  @override
  String deskEventClicks(int count, String point) {
    return '$count 击 $point';
  }

  @override
  String deskEventDown(String point) {
    return '按下 $point';
  }

  @override
  String deskEventMove(String point) {
    return '拖动 $point';
  }

  @override
  String deskEventPoint(String point) {
    return '指向 $point';
  }

  @override
  String deskEventRightClick(String point) {
    return '右键 $point';
  }

  @override
  String deskEventScroll(String dx, String dy) {
    return '滚动 $dx, $dy';
  }

  @override
  String deskEventScrollInertia(String dx, String dy) {
    return '滚动 $dx, $dy 惯性';
  }

  @override
  String deskEventText(String text) {
    return '文字 $text';
  }

  @override
  String deskEventUp(String point) {
    return '松开 $point';
  }

  @override
  String get deskFailBusy => '另一台手机正在看';

  @override
  String get deskFailCodec => '这台电脑不支持手机的视频编码';

  @override
  String get deskFailDisconnected => '连接已断开';

  @override
  String get deskFailGeneric => '连接失败';

  @override
  String get deskFailLan => '手机和这台 Mac 不在同一个局域网';

  @override
  String get deskFailNoPlugin => '这台设备不能看桌面';

  @override
  String get deskFailOutdated => '这台电脑的 Posse 需要更新';

  @override
  String get deskFailRoute => '直连和中转都不通';

  @override
  String get deskFailScreenPermission => '请到 Mac 上允许屏幕录制';

  @override
  String get deskForgetPassword => '忘掉密码';

  @override
  String get deskFpsStill => '静止';

  @override
  String get deskGestureClick => '单击';

  @override
  String get deskGestureDismiss => '点任意处关掉，模式在设置「看桌面」里换';

  @override
  String get deskGestureDrag => '拖动';

  @override
  String get deskGestureLongPressDrag => '长按到震一下，再拖';

  @override
  String get deskGestureMovePointer => '移动指针';

  @override
  String get deskGesturePan => '移动画面';

  @override
  String get deskGesturePinch => '双指捏合';

  @override
  String get deskGestureRightClick => '右键';

  @override
  String get deskGestureScroll => '滚动';

  @override
  String get deskGestureSlideOne => '单指滑动';

  @override
  String get deskGestureTap => '单指轻点';

  @override
  String get deskGestureTapAtPointer => '单指轻点，点在指针处';

  @override
  String get deskGestureThreeFingerDrag => '三指滑动，或轻点后马上按住滑';

  @override
  String get deskGestureTitle => '手势';

  @override
  String get deskGestureTwoFingerDrag => '双指拖动';

  @override
  String get deskGestureTwoFingerTap => '双指轻点';

  @override
  String get deskGestureZoom => '缩放';

  @override
  String get deskGestureZoomedDrag => '放大后单指拖动';

  @override
  String get deskHost => '主机';

  @override
  String get deskIndirect => '画面过不了隧道，请在同一个 Wi-Fi 下扫这台 Mac 的局域网码，或改用 Tailscale';

  @override
  String get deskKeyboard => '键盘';

  @override
  String get deskListingWindows => '正在列出窗口…';

  @override
  String get deskMacLocked => 'Mac 已锁屏';

  @override
  String get deskMainDisplay => '主显示器';

  @override
  String get deskMissionControl => '调度中心';

  @override
  String get deskModeDirect => '直接点按模式';

  @override
  String get deskModeTrackpad => '触控板模式';

  @override
  String get deskNoAccessibility => '能看不能操作，请到 Mac 上打开辅助功能';

  @override
  String get deskNoComputer => '没有连着的电脑';

  @override
  String get deskPasswordForgotten => '已忘掉记住的密码';

  @override
  String get deskPasswordHint => '登录密码';

  @override
  String get deskPasswordSaveFailed => '没能记住密码';

  @override
  String get deskPasswordSaved => '已记住密码，下次锁屏点「解锁」即可';

  @override
  String get deskPathLan => '直连';

  @override
  String get deskPathPunched => '打洞直连';

  @override
  String get deskPathRelay => '中转';

  @override
  String get deskPaused => '已暂停（后台）';

  @override
  String get deskPort => '端口';

  @override
  String get deskPortRange => '端口要在 1 到 65535';

  @override
  String get deskReconnect => '重新连接';

  @override
  String get deskRemember => '记住';

  @override
  String deskResolutionMain(String width, String height) {
    return '$width × $height · 主屏';
  }

  @override
  String get deskScreenPermissionHint =>
      'Mac 上打开「系统设置 › 隐私与安全性 › 录屏与系统录音」里的 Posse，再重试';

  @override
  String get deskShortcuts => '快捷键';

  @override
  String get deskSpaceLeft => '◀ 空间';

  @override
  String get deskSpaceRight => '空间 ▶';

  @override
  String get deskToLandscape => '切到横屏';

  @override
  String get deskToPortrait => '切到竖屏';

  @override
  String get deskUdpAddress => 'UDP 地址';

  @override
  String get deskUnlock => '解锁';

  @override
  String get deskUnlockNote => '输入 Mac 的登录密码，发到锁屏界面后自动回车';

  @override
  String get deskUnlockNoteRemember => '输入 Mac 的登录密码，发到锁屏界面后自动回车。勾「记住」只存在这台手机上';

  @override
  String get deskUse => '使用';

  @override
  String get deskViewTip => '画面';

  @override
  String get deskWaitingPicture => '已连上，等画面';

  @override
  String get deskWindowGone => '窗口关掉了或不在这个空间，回到整块屏';

  @override
  String get deskWindows => '窗口';

  @override
  String get deskWindowsEmpty => '这个空间里没有可以单独看的窗口';

  @override
  String get deskWindowsError => '列不出窗口，稍后再试';

  @override
  String get settingsLanguageSystem => '跟随系统';

  @override
  String get settingsLanguageTitle => '语言';

  @override
  String get usageCursorModels => 'Cursor 模型';

  @override
  String usageIncludedSpend(String used, String limit, String percent) {
    return '套餐内用量 $used / $limit（$percent）';
  }

  @override
  String get usageListSeparator => '，';

  @override
  String usageNamedWindow(String name, String window) {
    return '$name $window';
  }

  @override
  String get usageNotConnected => '电脑没有连上';

  @override
  String get usageOldPosse => '电脑上的 Posse 太旧，更新后才能刷新';

  @override
  String usageOnDemand(String used, String stale) {
    return '按需用量 $used$stale';
  }

  @override
  String usageOnDemandLimited(String used, String limit, String stale) {
    return '按需用量 $used，上限 $limit$stale';
  }

  @override
  String get usageOtherModels => '其他模型';

  @override
  String get usagePerMonth => '/月';

  @override
  String usageReadingAge(String reset, String time) {
    return '$reset，$time的读数';
  }

  @override
  String get usageRemaining => '剩余';

  @override
  String usageResetCards(int count, String stale) {
    return '重置卡 $count 张$stale';
  }

  @override
  String usageResetCardsExpiring(int count, String date, String stale) {
    return '重置卡 $count 张，最早一张 $date 过期$stale';
  }

  @override
  String usageShares(String shares, String stale) {
    return '本周用量用在 $shares$stale';
  }

  @override
  String get usageSheetNote =>
      '电脑上的 agent 运行时顺带更新；Cursor 用量和 Codex 重置卡本地没有，在它们用过之后电脑会查一次。';

  @override
  String usageShownPercent(String kind, String percent) {
    return '$kind $percent';
  }

  @override
  String usageStaleReading(String time) {
    return '（$time的读数）';
  }

  @override
  String get usageTitle => '套餐用量';

  @override
  String get usageUsed => '已用';

  @override
  String usageWindowDays(int count) {
    return '$count 天';
  }

  @override
  String get usageWindowFiveHours => '5 小时';

  @override
  String usageWindowHours(int count) {
    return '$count 小时';
  }

  @override
  String get usageWindowMonth => '每月';

  @override
  String get usageWindowMonthShort => '月';

  @override
  String get usageWindowPeriod => '本期';

  @override
  String get usageWindowWeek => '每周';

  @override
  String get usageWindowWeekShort => '周';
}
