/// 交给 Mac 的输入。这一步只记在本地，UDP 的 input 类型第 4 步再发。
sealed class DeskEvent {
  const DeskEvent();
}

class DeskTapEvent extends DeskEvent {
  const DeskTapEvent(this.at, this.clicks);

  final DeskPoint at;

  /// 1 单击，400 ms 内附近再点递增，对应 `kCGMouseEventClickState`。
  final int clicks;
}

class DeskRightClickEvent extends DeskEvent {
  const DeskRightClickEvent(this.at);

  final DeskPoint at;
}

enum DeskDragPhase { down, move, up }

class DeskDragEvent extends DeskEvent {
  const DeskDragEvent(this.phase, this.at);

  final DeskDragPhase phase;
  final DeskPoint at;
}

class DeskKeyEvent extends DeskEvent {
  const DeskKeyEvent(this.label);

  /// 键帽上的字，带修饰键时前面是 ⌘⇧⌥⌃。
  final String label;
}

class DeskTextEvent extends DeskEvent {
  const DeskTextEvent(this.text);

  final String text;
}

/// 只把 Mac 的指针移过去，不按键。双指滚动开始时先发一个：滚动交给指针下面的窗口。
class DeskPointEvent extends DeskEvent {
  const DeskPointEvent(this.at);

  final DeskPoint at;
}

/// 三指横滑：Mac 切到右边（next）或左边的空间（⌃→、⌃←）。
class DeskSpaceEvent extends DeskEvent {
  const DeskSpaceEvent({required this.next});

  final bool next;
}

class DeskScrollEvent extends DeskEvent {
  const DeskScrollEvent(this.dx, this.dy, {required this.inertia});

  /// 手指移动，逻辑像素，正方向是向右、向下。
  final double dx;
  final double dy;
  final bool inertia;
}

/// 整屏上的位置，各轴 0 到 1。
class DeskPoint {
  const DeskPoint(this.x, this.y);

  final double x;
  final double y;
}

/// 调试浮层上的一行。拖动的移动只留最新的一笔，免得把按下和松开挤掉。
class DeskEventLog {
  final events = <DeskEvent>[];
  static const keep = 6;

  void add(DeskEvent event) {
    if (event is DeskDragEvent &&
        event.phase == DeskDragPhase.move &&
        events.isNotEmpty &&
        events.last is DeskDragEvent &&
        (events.last as DeskDragEvent).phase == DeskDragPhase.move) {
      events[events.length - 1] = event;
      return;
    }
    events.add(event);
    if (events.length > keep) events.removeAt(0);
  }
}

String deskEventLine(DeskEvent event) {
  String fmt(DeskPoint p) =>
      '${p.x.toStringAsFixed(3)}, ${p.y.toStringAsFixed(3)}';
  return switch (event) {
    DeskTapEvent(:final at, :final clicks) =>
      clicks <= 1 ? '单击 ${fmt(at)}' : '$clicks 击 ${fmt(at)}',
    DeskRightClickEvent(:final at) => '右键 ${fmt(at)}',
    DeskDragEvent(:final phase, :final at) => switch (phase) {
      DeskDragPhase.down => '按下 ${fmt(at)}',
      DeskDragPhase.move => '拖动 ${fmt(at)}',
      DeskDragPhase.up => '松开 ${fmt(at)}',
    },
    DeskPointEvent(:final at) => '指向 ${fmt(at)}',
    DeskSpaceEvent(:final next) => next ? '右边的空间' : '左边的空间',
    DeskScrollEvent(:final dx, :final dy, :final inertia) =>
      '滚动 ${dx.toStringAsFixed(0)}, ${dy.toStringAsFixed(0)}'
          '${inertia ? ' 惯性' : ''}',
    DeskKeyEvent(:final label) => label,
    DeskTextEvent(:final text) => '文字 $text',
  };
}
