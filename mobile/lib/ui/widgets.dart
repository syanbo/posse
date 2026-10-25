import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';

import 'brand_paths.dart';
import 'svg_path.dart';
import 'theme.dart';

/// 所在页面的推入动画结束时完成，不在动画里就马上完成。
///
/// 进页时数据常在滑入途中到达，那一帧要建整页、排一屏文字，动画就顿一下。
/// 首次拿到的数据等这里完成再上屏；必须在 initState 之后调用。
Future<void> routeSettled(BuildContext context) {
  final animation = ModalRoute.of(context)?.animation;
  if (animation == null || animation.status != AnimationStatus.forward) {
    return Future.value();
  }
  final done = Completer<void>();
  void onStatus(AnimationStatus status) {
    if (status == AnimationStatus.forward) return;
    animation.removeStatusListener(onStatus);
    done.complete();
  }

  animation.addStatusListener(onStatus);
  return done.future;
}

final _brandPaths = <String, Path>{};

Path _brand(String d) => _brandPaths.putIfAbsent(d, () => parseSvgPath(d));

/// agent 的色块图标，颜色和桌面端 agentTile 一致；不是 agent 的终端画成中性底的提示符。
class AgentTile extends StatelessWidget {
  const AgentTile({super.key, required this.agent, this.size = 32});

  final String? agent;
  final double size;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final (String? path, Color bg, Color fg) = switch (agent) {
      'claude' => (brandClaudePath, const Color(0xFFD97757), Colors.white),
      'codex' => (brandCodexPath, Colors.white, Colors.black),
      'grok' => (brandGrokPath, c.text, c.card),
      'cursor' => (brandCursorPath, const Color(0xFF6E56CF), Colors.white),
      _ => (null, c.fill, c.muted),
    };
    return Container(
      width: size,
      height: size,
      decoration: BoxDecoration(
        color: bg,
        borderRadius: BorderRadius.circular(size * 0.26),
        // Codex 的白底在浅色卡片上会融掉，描一圈细边（和桌面端一致）
        border: agent == 'codex'
            ? Border.all(color: const Color(0x1F000000))
            : null,
      ),
      alignment: Alignment.center,
      child: path == null
          ? Icon(Icons.terminal_rounded, size: size * 0.58, color: fg)
          : CustomPaint(
              size: Size.square(size * 0.62),
              painter: _PathPainter(_brand(path), fg),
            ),
    );
  }
}

class _PathPainter extends CustomPainter {
  _PathPainter(this.path, this.color);

  final Path path;
  final Color color;

  @override
  void paint(Canvas canvas, Size size) {
    canvas.scale(size.width / 24, size.height / 24);
    canvas.drawPath(path, Paint()..color = color);
  }

  @override
  bool shouldRepaint(_PathPainter old) =>
      old.path != path || old.color != color;
}

/// agent 的状态。工作区的 permission 和标签的 waiting 都是待处理。
enum AgentState { working, waiting, done, idle, none }

AgentState agentStateOf(String? state) => switch (state) {
  'working' || 'starting' => AgentState.working,
  'waiting' || 'permission' => AgentState.waiting,
  'done' || 'idle' => AgentState.done,
  'active' => AgentState.idle,
  _ => AgentState.none,
};

final _hex = parseSvgPath('M12 2.75 20 7.375v9.25L12 21.25 4 16.625v-9.25z');

/// 状态图标，和桌面端 statusGlyph 一致：工作中是 Claude Code 那样来回变换的陶土色星号（减少动态效果时
/// 退回静止的实心蜂巢格，用中性色），空心带圆点的琥珀色是待处理，空心的绿色是本轮完成。
class StatusHex extends StatelessWidget {
  const StatusHex({super.key, required this.state, this.size = 16, this.color});

  final AgentState state;
  final double size;
  final Color? color;

  @override
  Widget build(BuildContext context) {
    if (state == AgentState.none) return SizedBox.square(dimension: size);
    final c = HiveColors.of(context);
    if (state == AgentState.working &&
        this.color == null &&
        !MediaQuery.disableAnimationsOf(context)) {
      return WorkingStar(size: size, color: c.accent);
    }
    final color =
        this.color ??
        switch (state) {
          AgentState.working => c.muted,
          AgentState.waiting => c.honey,
          AgentState.done => c.sage,
          _ => c.faint,
        };
    return CustomPaint(
      size: Size.square(size),
      painter: _HexPainter(state, color),
    );
  }
}

/// 星号的帧和桌面端 starFrames 相同，每 160ms 换一个字符。
const _starFrames = ['·', '✢', '✳', '✶', '✻', '✽', '✻', '✶', '✳', '✢'];
const _starFrame = Duration(milliseconds: 160);

/// 所有工作中的星号共用一个时钟，一起变；没有星号在屏幕上时停掉计时器。
class _StarClock extends ValueNotifier<int> {
  _StarClock() : super(0);

  Timer? _timer;

  @override
  void addListener(VoidCallback listener) {
    super.addListener(listener);
    _timer ??= Timer.periodic(_starFrame, (_) => value++);
  }

  @override
  void removeListener(VoidCallback listener) {
    super.removeListener(listener);
    if (!hasListeners) {
      _timer?.cancel();
      _timer = null;
    }
  }
}

final _starClock = _StarClock();

class WorkingStar extends StatelessWidget {
  const WorkingStar({super.key, required this.size, required this.color});

  final double size;
  final Color color;

  @override
  Widget build(BuildContext context) {
    return SizedBox.square(
      dimension: size,
      child: ValueListenableBuilder<int>(
        valueListenable: _starClock,
        builder: (context, tick, _) {
          var glyph = _starFrames[tick % _starFrames.length];
          // Android 把 ✳（默认 emoji 样式）交给彩色 emoji 字体、不认 U+FE0E，会画成绿色。
          // 换成同样是星号、只有文字样式的 ✱，系统的符号字体里有
          if (glyph == '✳' && defaultTargetPlatform == TargetPlatform.android) {
            glyph = '✱';
          }
          // 行高 1、上下均分时星号的墨迹本来就在正中，不用像桌面端那样往上挪
          // 字号比格子大一点，放在 OverflowBox 里居中，不被格子挤得折行
          return OverflowBox(
            maxWidth: size * 2,
            maxHeight: size * 2,
            child: Text(
              // U+FE0E 要文字样式：不然 iOS 把 ✳ 画成绿色 emoji
              '$glyph\uFE0E',
              textAlign: TextAlign.center,
              textHeightBehavior: const TextHeightBehavior(
                leadingDistribution: TextLeadingDistribution.even,
              ),
              style: TextStyle(
                fontSize: size * 1.15,
                height: 1,
                color: color,
                fontFamilyFallback: const ['Apple Symbols', 'Menlo'],
              ),
            ),
          );
        },
      ),
    );
  }
}

class _HexPainter extends CustomPainter {
  _HexPainter(this.state, this.color);

  final AgentState state;
  final Color color;

  @override
  void paint(Canvas canvas, Size size) {
    canvas.scale(size.width / 24, size.height / 24);
    final paint = Paint()
      ..color = color
      ..strokeWidth = 2.2
      ..strokeJoin = StrokeJoin.round
      ..style = state == AgentState.working
          ? PaintingStyle.fill
          : PaintingStyle.stroke;
    canvas.drawPath(_hex, paint);
    if (state == AgentState.waiting) {
      canvas.drawCircle(const Offset(12, 12), 3, Paint()..color = color);
    }
  }

  @override
  bool shouldRepaint(_HexPainter old) =>
      old.state != state || old.color != color;
}

/// 状态的文字。只有待处理用琥珀色，其余用次要色。
String agentStateText(AgentState state) => switch (state) {
  AgentState.working => '工作中',
  AgentState.waiting => '待处理',
  AgentState.done => '完成',
  AgentState.idle => '空闲',
  AgentState.none => '',
};

/// 弹框：从略小放大到原大、同时淡入，收起时缩回去、比出来快。Material 默认只淡入一下，看着生硬。
/// 系统开了减少动态效果时只淡入淡出。
Future<T?> showHiveDialog<T>({
  required BuildContext context,
  required WidgetBuilder builder,
}) {
  return Navigator.of(context, rootNavigator: true).push<T>(
    _HiveDialogRoute<T>(
      barrierLabel: MaterialLocalizations.of(context).modalBarrierDismissLabel,
      pageBuilder: (dialog, _, _) => SafeArea(child: Builder(builder: builder)),
      transitionBuilder: (dialog, animation, _, child) {
        final curved = CurvedAnimation(
          parent: animation,
          curve: Curves.easeOutCubic,
          reverseCurve: Curves.easeInCubic,
        );
        if (MediaQuery.disableAnimationsOf(dialog)) {
          return FadeTransition(opacity: curved, child: child);
        }
        return FadeTransition(
          opacity: curved,
          child: ScaleTransition(
            scale: Tween(begin: 0.9, end: 1.0).animate(curved),
            child: child,
          ),
        );
      },
    ),
  );
}

class _HiveDialogRoute<T> extends RawDialogRoute<T> {
  _HiveDialogRoute({
    required super.pageBuilder,
    required super.transitionBuilder,
    super.barrierLabel,
  }) : super(
         barrierColor: Colors.black54,
         transitionDuration: const Duration(milliseconds: 240),
       );

  @override
  Duration get reverseTransitionDuration => const Duration(milliseconds: 160);
}

/// 列表分组：上面一行小标题，下面一张卡片，行之间是缩进的细线。
class Section extends StatelessWidget {
  const Section({
    super.key,
    this.title,
    this.trailing,
    required this.children,
    this.inset = 16,
    this.margin = const EdgeInsets.fromLTRB(16, 0, 16, 20),
    this.folded,
  });

  final String? title;
  final Widget? trailing;
  final List<Widget> children;

  /// 不为空时，children 只有第一行（标题行）常显，其余行收在可展开的一块里：
  /// true 收起、false 展开，切换时上下滑动。
  final bool? folded;

  /// 分隔线左边留出的宽度，对齐行里的文字而不是图标。
  final double inset;

  /// 卡片外边距。放在已经有左右内边距的页面里时传 bottom-only。
  final EdgeInsets margin;

  List<Widget> _rows(HiveColors c, List<Widget> rows, {required bool first}) =>
      _rowsImpl(c, rows, inset, first: first);

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    return Padding(
      padding: margin,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          if (title != null) _SectionTitle(title!, trailing: trailing),
          Container(
            decoration: BoxDecoration(
              color: c.card,
              borderRadius: BorderRadius.circular(Rd.card),
            ),
            clipBehavior: Clip.antiAlias,
            child: folded == null
                ? Column(children: _rows(c, children, first: true))
                : Column(
                    children: [
                      children.first,
                      Fold(
                        open: !folded!,
                        child: Column(
                          children: _rows(c, children.sublist(1), first: false),
                        ),
                      ),
                    ],
                  ),
          ),
        ],
      ),
    );
  }
}

class _SectionTitle extends StatelessWidget {
  const _SectionTitle(this.title, {this.trailing});

  final String title;
  final Widget? trailing;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    return Padding(
      padding: const EdgeInsets.fromLTRB(4, 0, 4, 8),
      child: Row(
        children: [
          Expanded(
            child: Text(
              title,
              style: TextStyle(
                fontSize: Fs.meta,
                fontWeight: FontWeight.w500,
                color: c.muted,
              ),
            ),
          ),
          ?trailing,
        ],
      ),
    );
  }
}

/// 和 [Section] 一样的卡片，放在 CustomScrollView 里按需建行：几百行的列表一次全建，
/// 数据到的那一帧要几十毫秒。
class SliverSection extends StatelessWidget {
  const SliverSection({
    super.key,
    this.title,
    required this.itemCount,
    required this.itemBuilder,
    this.inset = 16,
    this.margin = const EdgeInsets.fromLTRB(16, 0, 16, 20),
  });

  final String? title;
  final int itemCount;
  final IndexedWidgetBuilder itemBuilder;
  final double inset;
  final EdgeInsets margin;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    const radius = Radius.circular(Rd.card);
    return SliverPadding(
      padding: margin,
      sliver: SliverMainAxisGroup(
        slivers: [
          if (title != null) SliverToBoxAdapter(child: _SectionTitle(title!)),
          DecoratedSliver(
            decoration: BoxDecoration(
              color: c.card,
              borderRadius: BorderRadius.circular(Rd.card),
            ),
            sliver: SliverList.separated(
              itemCount: itemCount,
              separatorBuilder: (_, _) => Padding(
                padding: EdgeInsets.only(left: inset),
                child: Divider(height: 1, thickness: 1, color: c.line),
              ),
              // 装饰不裁剪，首尾两行自己裁成圆角，按下的底色不溢出卡片
              itemBuilder: (context, i) {
                final row = itemBuilder(context, i);
                final first = i == 0;
                final last = i == itemCount - 1;
                if (!first && !last) return row;
                return ClipRRect(
                  borderRadius: BorderRadius.vertical(
                    top: first ? radius : Radius.zero,
                    bottom: last ? radius : Radius.zero,
                  ),
                  child: row,
                );
              },
            ),
          ),
        ],
      ),
    );
  }
}

/// 行之间画分隔线；first 为假时第一行上面也画（接在常显的标题行后面）。
List<Widget> _rowsImpl(
  HiveColors c,
  List<Widget> rows,
  double inset, {
  required bool first,
}) => [
  for (var i = 0; i < rows.length; i++) ...[
    if (i > 0 || !first)
      Padding(
        padding: EdgeInsets.only(left: inset),
        child: Divider(height: 1, thickness: 1, color: c.line),
      ),
    rows[i],
  ],
];

/// 展开收起时上下滑动的一块，收起后不占高度。减少动态效果时直接切换。
class Fold extends StatefulWidget {
  const Fold({super.key, required this.open, required this.child});

  final bool open;
  final Widget child;

  @override
  State<Fold> createState() => _FoldState();
}

class _FoldState extends State<Fold> with SingleTickerProviderStateMixin {
  late final _ctrl = AnimationController(
    vsync: this,
    duration: const Duration(milliseconds: 220),
    value: widget.open ? 1 : 0,
  );
  late final _curve = CurvedAnimation(
    parent: _ctrl,
    curve: Curves.easeOutCubic,
    reverseCurve: Curves.easeInCubic,
  );

  @override
  void didUpdateWidget(Fold old) {
    super.didUpdateWidget(old);
    if (old.open == widget.open) return;
    if (MediaQuery.disableAnimationsOf(context)) {
      _ctrl.value = widget.open ? 1 : 0;
    } else if (widget.open) {
      _ctrl.forward();
    } else {
      _ctrl.reverse();
    }
  }

  @override
  void dispose() {
    _ctrl.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => AnimatedBuilder(
    animation: _ctrl,
    // 收起停稳后不再构建里面的行
    builder: (context, child) => _ctrl.value == 0
        ? const SizedBox(width: double.infinity)
        : ClipRect(
            child: Align(
              alignment: Alignment.topCenter,
              heightFactor: _curve.value,
              child: Opacity(opacity: _curve.value, child: child),
            ),
          ),
    child: widget.child,
  );
}

/// 卡片里的一行：左边图标，中间标题和副标题，右边状态。按下是中性的淡底。
class RowTile extends StatelessWidget {
  const RowTile({
    super.key,
    this.leading,
    required this.title,
    this.subtitle,
    this.trailing,
    this.onTap,
    this.onLongPress,
    this.chevron = true,
  });

  final Widget? leading;
  final Widget title;
  final Widget? subtitle;
  final Widget? trailing;
  final VoidCallback? onTap;
  final VoidCallback? onLongPress;
  final bool chevron;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    return InkWell(
      onTap: onTap,
      onLongPress: onLongPress,
      highlightColor: c.wash,
      child: ConstrainedBox(
        constraints: const BoxConstraints(minHeight: 60),
        child: Padding(
          padding: const EdgeInsets.fromLTRB(14, 10, 10, 10),
          child: Row(
            children: [
              if (leading != null) ...[leading!, const SizedBox(width: 12)],
              Expanded(
                child: Column(
                  mainAxisSize: MainAxisSize.min,
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    DefaultTextStyle.merge(
                      style: TextStyle(
                        fontSize: Fs.body,
                        fontWeight: FontWeight.w500,
                        color: c.text,
                      ),
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      child: title,
                    ),
                    if (subtitle != null) ...[
                      const SizedBox(height: 3),
                      DefaultTextStyle.merge(
                        style: TextStyle(fontSize: Fs.meta, color: c.muted),
                        maxLines: 2,
                        overflow: TextOverflow.ellipsis,
                        child: subtitle!,
                      ),
                    ],
                  ],
                ),
              ),
              if (trailing != null) ...[const SizedBox(width: 8), trailing!],
              if (chevron && onTap != null)
                Padding(
                  padding: const EdgeInsets.only(left: 4),
                  child: Icon(
                    Icons.chevron_right_rounded,
                    size: 20,
                    color: c.faint,
                  ),
                ),
            ],
          ),
        ),
      ),
    );
  }
}

/// 设置页和会话面板共用的开关。系统开关在卡片里显得大，缩到八成，打开时轨道用强调色。
class HiveSwitch extends StatelessWidget {
  const HiveSwitch({super.key, required this.value, required this.onChanged});

  final bool value;
  final ValueChanged<bool> onChanged;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    return Transform.scale(
      scale: 0.8,
      child: Switch.adaptive(
        value: value,
        activeTrackColor: c.accent,
        onChanged: onChanged,
      ),
    );
  }
}

/// 分段选择：填充底，选中段是陶土色填充。
class Segmented extends StatelessWidget {
  const Segmented({
    super.key,
    required this.labels,
    required this.selected,
    required this.onChanged,
  });

  final List<String> labels;
  final int selected;
  final ValueChanged<int> onChanged;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    return Container(
      height: 36,
      padding: const EdgeInsets.all(3),
      decoration: BoxDecoration(
        color: c.fill,
        borderRadius: BorderRadius.circular(Rd.input),
      ),
      child: Row(
        children: [
          for (var i = 0; i < labels.length; i++)
            Expanded(
              child: GestureDetector(
                behavior: HitTestBehavior.opaque,
                onTap: () => onChanged(i),
                child: AnimatedContainer(
                  duration: const Duration(milliseconds: 150),
                  curve: Curves.easeOut,
                  alignment: Alignment.center,
                  decoration: BoxDecoration(
                    color: i == selected ? c.accent : Colors.transparent,
                    borderRadius: BorderRadius.circular(Rd.input - 3),
                  ),
                  child: Text(
                    labels[i],
                    style: TextStyle(
                      fontSize: 14,
                      fontWeight: FontWeight.w500,
                      color: i == selected ? Colors.white : c.muted,
                    ),
                  ),
                ),
              ),
            ),
        ],
      ),
    );
  }
}

/// 页面中间的空状态：图标、一句说明，可选一个按钮。
class EmptyState extends StatelessWidget {
  const EmptyState({
    super.key,
    required this.icon,
    required this.text,
    this.action,
    this.error = false,
  });

  final IconData icon;
  final String text;
  final Widget? action;
  final bool error;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(32),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(icon, size: 36, color: error ? c.danger : c.faint),
            const SizedBox(height: 12),
            Text(
              text,
              textAlign: TextAlign.center,
              style: TextStyle(
                fontSize: 14,
                height: 1.5,
                color: error ? c.danger : c.muted,
              ),
            ),
            if (action != null) ...[const SizedBox(height: 16), action!],
          ],
        ),
      ),
    );
  }
}

/// 错误条：一行文字，留在原处，等下一次成功时消失。
class NoticeBar extends StatelessWidget {
  const NoticeBar(this.text, {super.key, this.onClose, this.floating = false});

  final String text;

  /// 点一下关掉。
  final VoidCallback? onClose;

  /// 浮在内容上面（放进 Stack 用）：底色不透明、带阴影，出现消失不挪动下面的终端或列表。
  final bool floating;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final tint = c.danger.withValues(alpha: c.dark ? 0.16 : 0.08);
    final bar = Container(
      margin: floating
          ? const EdgeInsets.symmetric(horizontal: 16)
          : const EdgeInsets.fromLTRB(16, 4, 16, 8),
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
      decoration: BoxDecoration(
        color: floating ? Color.alphaBlend(tint, c.card) : tint,
        borderRadius: BorderRadius.circular(Rd.input),
        boxShadow: floating
            ? [
                BoxShadow(
                  color: Colors.black.withValues(alpha: c.dark ? 0.4 : 0.12),
                  blurRadius: 16,
                  offset: const Offset(0, 4),
                ),
              ]
            : null,
      ),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Icon(Icons.error_outline_rounded, size: 18, color: c.danger),
          const SizedBox(width: 8),
          Expanded(
            child: Text(
              text,
              style: TextStyle(fontSize: 13, height: 1.4, color: c.danger),
            ),
          ),
        ],
      ),
    );
    if (onClose == null) return bar;
    return GestureDetector(
      behavior: HitTestBehavior.opaque,
      onTap: onClose,
      child: bar,
    );
  }
}

/// 发图片、文件的进度：第几张、传了多少。浮在内容上面（放进 Stack 用），出现和消失都不挪动列表或终端，
/// 终端也就不会因此改尺寸、让 agent 重排。
class UploadBar extends StatelessWidget {
  const UploadBar({
    super.key,
    required this.index,
    required this.count,
    required this.done,
    this.what = '图片',
  });

  /// 传的是什么，写在「正在发送」后面。
  final String what;

  /// 正在传第几张（从 0 数）。
  final int index;
  final int count;

  /// 这一张传了多少，0 到 1。
  final double done;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final which = count > 1 ? ' ${index + 1}/$count' : '';
    // 整体进度按张数摊开，多张时进度条一路走到底，不在每张开头跳回去
    final overall = count == 0 ? 0.0 : (index + done) / count;
    return IgnorePointer(
      child: Center(
        child: Container(
          width: 220,
          padding: const EdgeInsets.fromLTRB(12, 8, 12, 10),
          decoration: BoxDecoration(
            color: c.card,
            borderRadius: BorderRadius.circular(Rd.card),
            border: Border.all(color: c.line),
            boxShadow: [
              BoxShadow(
                color: Colors.black.withValues(alpha: c.dark ? 0.4 : 0.12),
                blurRadius: 16,
                offset: const Offset(0, 4),
              ),
            ],
          ),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Row(
                children: [
                  Expanded(
                    child: Text(
                      '正在发送$what$which',
                      style: TextStyle(fontSize: Fs.caption, color: c.text),
                    ),
                  ),
                  Text(
                    '${(done * 100).round()}%',
                    style: TextStyle(
                      fontSize: Fs.caption,
                      color: c.muted,
                      fontFeatures: const [FontFeature.tabularFigures()],
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 6),
              ClipRRect(
                borderRadius: BorderRadius.circular(2),
                child: LinearProgressIndicator(
                  value: overall,
                  minHeight: 3,
                  color: c.accent,
                  backgroundColor: c.fill,
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

/// 一次性的提示（剪贴板是空的、发送失败……）过一会儿自己消失，不一直挡在输入框上面。
const noticeLife = Duration(seconds: 4);

/// 终端标题里 agent 自己加的状态符号（Claude 的 ✳、转圈的盲文字符）。界面另有状态图标，标题里不再重复。
String cleanTitle(String title) {
  final trimmed = title.replaceFirst(RegExp(r'^[\s⠀-⣿︎️✳✶✻✽✢·*⏺●○◐◓◑◒]+'), '');
  return trimmed.trim().isEmpty ? title.trim() : trimmed.trim();
}

/// 用户目录换成 ~，路径短一截。
String tildePath(String path) =>
    path.replaceFirst(RegExp(r'^/Users/[^/]+(?=/|$)'), '~');

/// 文件大小，按 1024 进位：12 B、3.4 KB、5.6 MB。
String formatBytes(int n) {
  if (n < 1024) return '$n B';
  if (n < 1024 * 1024) return '${(n / 1024).toStringAsFixed(1)} KB';
  return '${(n / 1024 / 1024).toStringAsFixed(1)} MB';
}

/// 立即发送（Ctrl+Enter）的键帽：⌃ 和 ↩ 用两个 Material 图标拼，笔画和别的图标键一致；
/// 写成字符时两个符号都要退到别的字体，粗细、基线对不上。
class SendNowGlyph extends StatelessWidget {
  const SendNowGlyph({super.key, this.size = 18, this.color});

  final double size;
  final Color? color;

  @override
  Widget build(BuildContext context) {
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        Icon(
          Icons.keyboard_control_key_rounded,
          size: size * 0.85,
          color: color,
        ),
        Icon(Icons.keyboard_return_rounded, size: size, color: color),
      ],
    );
  }
}
