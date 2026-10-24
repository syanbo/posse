import 'package:flutter/painting.dart';
import 'package:flutter/services.dart';

import 'events.dart';

/// 方向锁和边缘手势是进程里的全局状态。页面滑出要等转场结束才 dispose，
/// 这时新页面可能已经锁上了。只有当前持有者离开才恢复。
class DeskChromeLease {
  Object? _holder;

  void acquire(Object holder) => _holder = holder;

  /// 还是自己持有才清掉并返回 true。已经被下一页接走时什么都不做。
  bool release(Object holder) {
    if (!identical(_holder, holder)) return false;
    _holder = null;
    return true;
  }
}

final deskChromeLease = DeskChromeLease();

/// 展开后这么久没有点、没有拖，胶囊收回去。再长就一直挡着顶上一行。
const deskBarIdle = Duration(seconds: 4);

/// 横屏快捷键栏的键高。竖屏仍是 [deskKeyBarBody]。
const deskKeyBarDense = 32.0;

/// 看桌面那条的开关。位置不在这里：竖屏和横屏各用一个值，页面自己记，不写入设置。
class DeskChrome {
  var expanded = false;
  var shortcuts = false;
  Duration? idleFrom;

  /// 自己打开了快捷键，或系统键盘正开着（扩展键行贴在键盘上）。
  bool keysVisible(bool keyboardUp) => shortcuts || keyboardUp;

  void toggleExpanded(Duration now) {
    expanded = !expanded;
    idleFrom = expanded ? now : null;
  }

  void noteActivity(Duration now) {
    if (expanded) idleFrom = now;
  }

  void toggleShortcuts() => shortcuts = !shortcuts;

  /// 到点了才收。返回有没有收掉，好让界面决定要不要重建。
  bool collapseIfIdle(Duration now) {
    final from = idleFrom;
    if (!expanded || from == null || now - from < deskBarIdle) return false;
    expanded = false;
    idleFrom = null;
    return true;
  }
}

/// 对齐用的 -1 到 1。0.5 是正中。
double deskBarAlignment(double anchor) => (anchor * 2 - 1).clamp(-1.0, 1.0);

/// 拖动时手指移动占「轨道长度减去胶囊在该方向上的长度」的比例。
double deskBarTrack(double safeExtent, double barExtent) =>
    (safeExtent - barExtent).clamp(1.0, double.infinity);

/// 横屏胶囊放在没有刘海的那一侧。左右安全区一样时靠右。
bool deskBarOnRight({required double left, required double right}) =>
    left >= right;

/// 横屏画面让开左右刘海，宽度铺满剩下的。竖屏用整块舞台。
EdgeInsets deskPicturePadding({
  required bool landscape,
  required EdgeInsets viewPadding,
}) {
  if (!landscape) return EdgeInsets.zero;
  return EdgeInsets.only(left: viewPadding.left, right: viewPadding.right);
}

/// 进页面锁住的方向。横屏给两个方向，躺哪边都行。
List<DeviceOrientation> deskLockedOrientations(bool landscape) => landscape
    ? const [DeviceOrientation.landscapeLeft, DeviceOrientation.landscapeRight]
    : const [DeviceOrientation.portraitUp];

/// 离开页面传空列表。iOS 上 Flutter 把空列表收成全部方向，系统再和 Info.plist
/// 取交集：iPhone 是竖屏加两个横屏，iPad 还留着倒置竖屏。写死三项会把 iPad 的倒置拿掉。
const deskAppOrientations = <DeviceOrientation>[];

Future<void> lockDeskOrientation(bool landscape) {
  return SystemChrome.setPreferredOrientations(
    deskLockedOrientations(landscape),
  );
}

Future<void> restoreDeskOrientation() {
  return SystemChrome.setPreferredOrientations(deskAppOrientations);
}

/// 右键没有指针，靠这一下知道点中了。长按进入拖动另走手势的 onArm，不在事件里。
bool deskHapticFor(DeskEvent event) => event is DeskRightClickEvent;

/// 还没收到视频时不写 0。
/// 调试面板里画面走的路，取值见 [DeskStats.path]。
String deskPathLabel(String? path) => switch (path) {
  'lan' => '直连',
  'relay' => '中转',
  'punched' => '打洞直连',
  _ => '—',
};

String deskFpsLabel(double fps, {required bool video}) {
  if (!video) return '— fps';
  return '${fps.toStringAsFixed(0)} fps';
}

/// 键盘弹出时画面上移多少：最后点的那一处（多半是要打字的输入框）露在被盖住的部分上面，
/// 留一行字的余量。没点过、或本来就露着时不动，最多移被盖住的那么高。
double deskLift({
  required double? tapY,
  required double screenHeight,
  required double covered,
}) {
  if (tapY == null || covered <= 0) return 0;
  const margin = 48.0;
  final need = tapY + margin - (screenHeight - covered);
  return need.clamp(0.0, covered);
}
