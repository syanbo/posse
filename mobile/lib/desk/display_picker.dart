import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'displays.dart';

/// 选好的画面：整块屏或一个窗口，[id] 是屏的 ID 或窗口的 ID。
typedef DeskPick = ({bool window, int id});

/// 选看什么：上面是屏（多块时按真实排布画出各块，块里写名字和分辨率，点哪块切哪块），下面是当前空间里的窗口，
/// [windows] 打开时现要。最上面是清晰度和帧率（[fps] 为 null 时电脑不支持手选、不显示，0 是自动），
/// 选了经 [onFps] 立即交出去、面板不关。返回选中的屏或窗口，取消为 null。
Future<DeskPick?> pickDeskView(
  BuildContext context,
  DeskDisplays displays, {
  required Future<List<DeskWindow>> windows,
  ValueListenable<int>? generation,
  ValueListenable<DeskDisplays>? target,
  int? resolution,
  Future<void> Function(int)? onResolution,
  int? fps,
  ValueChanged<int>? onFps,
}) async {
  BuildContext? sheetContext;
  final openedAt = generation?.value;
  void invalidate() {
    final current = target?.value ?? displays;
    // 目标变化后旧勾选已失效，收起面板让回退提示可见；尺寸变化不影响选择。
    if (generation?.value == openedAt &&
        current.current == displays.current &&
        current.window?.id == displays.window?.id) {
      return;
    }
    // 生命周期回调和页面销毁都可能发生在导航锁内，只移除这张旧面板。
    WidgetsBinding.instance.addPostFrameCallback((_) {
      final sheet = sheetContext;
      if (sheet == null || !sheet.mounted) return;
      final route = ModalRoute.of(sheet);
      if (route != null && route.isActive) {
        Navigator.of(sheet).removeRoute(route);
      }
    });
    WidgetsBinding.instance.ensureVisualUpdate();
  }

  generation?.addListener(invalidate);
  target?.addListener(invalidate);
  try {
    return await showModalBottomSheet<DeskPick>(
      context: context,
      showDragHandle: true,
      // 看桌面常是横屏，默认的高度上限放不下排布图加列表
      isScrollControlled: true,
      builder: (sheet) {
        sheetContext = sheet;
        invalidate();
        final c = PosseColors.of(sheet);
        void pick(DeskPick p) => Navigator.of(sheet).pop(p);
        // 只看窗口时屏上不打勾，排布图照样标出窗口所在的那块
        final watching = displays.window;
        // 面板高度固定、内容在里面滚：窗口列表常在弹出动画途中才到，跟着内容长高面板会跳一下
        final height = (MediaQuery.sizeOf(sheet).height * 0.6).clamp(
          200.0,
          460.0,
        );
        return SafeArea(
          top: false,
          child: SizedBox(
            height: height,
            child: _StreamChoices(
              resolution: resolution,
              onResolution: onResolution,
              fps: fps,
              onFps: onFps,
              child: SingleChildScrollView(
                child: Column(
                  children: [
                    Section(
                      title: sheet.l10n.deskEntireScreen,
                      inset: 48,
                      children: [
                        // 多块屏只画排布图，名字和分辨率写在各块里
                        if (displays.several)
                          Padding(
                            padding: const EdgeInsets.fromLTRB(16, 12, 16, 12),
                            child: SizedBox(
                              height: 150,
                              child: DeskDisplayMap(
                                displays: displays,
                                onPick: (id) => pick((window: false, id: id)),
                              ),
                            ),
                          )
                        else
                          for (final d in displays.list)
                            RowTile(
                              key: Key('desk-display-${d.id}'),
                              leading: Icon(
                                Icons.desktop_windows_rounded,
                                size: Ic.medium,
                                color: d.id == displays.current
                                    ? c.accent
                                    : c.muted,
                              ),
                              title: Text(
                                deskDisplayName(sheet.l10n, displays, d),
                              ),
                              subtitle: Text(_resolution(sheet.l10n, d)),
                              trailing:
                                  watching == null && d.id == displays.current
                                  ? Icon(
                                      Icons.check_rounded,
                                      size: Ic.medium,
                                      color: c.accent,
                                    )
                                  : null,
                              chevron: false,
                              onTap: () => pick((window: false, id: d.id)),
                            ),
                      ],
                    ),
                    FutureBuilder<List<DeskWindow>>(
                      future: windows,
                      builder: (context, snap) {
                        final list = snap.data;
                        return Section(
                          title: context.l10n.deskWindows,
                          inset: 48,
                          children: [
                            if (snap.connectionState != ConnectionState.done)
                              _Note(
                                key: const Key('desk-windows-loading'),
                                context.l10n.deskListingWindows,
                              )
                            else if (snap.hasError)
                              _Note(context.l10n.deskWindowsError)
                            else if (list == null || list.isEmpty)
                              _Note(context.l10n.deskWindowsEmpty)
                            else
                              for (final w in list)
                                RowTile(
                                  key: Key('desk-window-${w.id}'),
                                  leading: _AppIcon(
                                    w,
                                    color: w.id == watching?.id
                                        ? c.accent
                                        : c.muted,
                                  ),
                                  title: Text(
                                    w.label,
                                    maxLines: 1,
                                    overflow: TextOverflow.ellipsis,
                                  ),
                                  subtitle: Text(
                                    w.title.isNotEmpty
                                        ? w.app
                                        : '${w.width.round()} × ${w.height.round()}',
                                    maxLines: 1,
                                    overflow: TextOverflow.ellipsis,
                                  ),
                                  trailing: w.id == watching?.id
                                      ? Icon(
                                          Icons.check_rounded,
                                          size: Ic.medium,
                                          color: c.accent,
                                        )
                                      : null,
                                  chevron: false,
                                  onTap: () => pick((window: true, id: w.id)),
                                ),
                          ],
                        );
                      },
                    ),
                  ],
                ),
              ),
            ),
          ),
        );
      },
    );
  } finally {
    generation?.removeListener(invalidate);
    target?.removeListener(invalidate);
  }
}

class _StreamChoices extends StatefulWidget {
  const _StreamChoices({
    required this.child,
    this.resolution,
    this.onResolution,
    this.fps,
    this.onFps,
  });

  final Widget child;
  final int? resolution;
  final Future<void> Function(int)? onResolution;
  final int? fps;
  final ValueChanged<int>? onFps;

  @override
  State<_StreamChoices> createState() => _StreamChoicesState();
}

class _StreamChoicesState extends State<_StreamChoices> {
  late int _resolution = widget.resolution ?? 0;
  late int _fps = widget.fps ?? 0;
  Object? _error;
  int _generation = 0;
  bool? _editingResolution;

  void _toggle(bool resolution) => setState(() {
    _editingResolution = _editingResolution == resolution ? null : resolution;
  });

  Future<void> _chooseResolution(int value) async {
    if (value == _resolution && _error == null) return;
    final gen = ++_generation;
    setState(() {
      _resolution = value;
      _error = null;
    });
    try {
      await widget.onResolution!(value);
    } on Object catch (error) {
      // 旧请求的迟到错误不覆盖后来的选择；偏好仍保留，下次订阅重试。
      if (mounted && gen == _generation) setState(() => _error = error);
    }
  }

  @override
  Widget build(BuildContext context) {
    final resolutionLabels = {
      0: context.l10n.deskQualityAuto,
      for (final v in deskResolutionChoices) v: '${v}p',
    };
    final fpsLabels = {
      0: context.l10n.deskFrameRateAuto,
      for (final v in deskFpsChoices) v: '$v',
    };
    final c = PosseColors.of(context);
    final reduceMotion = MediaQuery.disableAnimationsOf(context);
    final editing = _editingResolution;
    final hasChoices =
        (widget.resolution != null && widget.onResolution != null) ||
        (widget.fps != null && widget.onFps != null);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (hasChoices)
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
            child: Wrap(
              spacing: 8,
              runSpacing: 8,
              children: [
                if (widget.resolution != null && widget.onResolution != null)
                  _StreamChoice(
                    key: const Key('desk-resolution'),
                    title: context.l10n.deskQuality,
                    value:
                        resolutionLabels[_resolution] ?? resolutionLabels[0]!,
                    values: resolutionLabels.values,
                    expanded: editing == true,
                    onPressed: () => _toggle(true),
                  ),
                if (widget.fps != null && widget.onFps != null)
                  _StreamChoice(
                    key: const Key('desk-fps'),
                    title: context.l10n.deskFrameRate,
                    value: fpsLabels[_fps] ?? fpsLabels[0]!,
                    values: fpsLabels.values,
                    expanded: editing == false,
                    onPressed: () => _toggle(false),
                  ),
              ],
            ),
          ),
        Expanded(
          child: LayoutBuilder(
            builder: (context, constraints) => Stack(
              fit: StackFit.expand,
              children: [
                widget.child,
                if (editing != null)
                  GestureDetector(
                    key: const Key('desk-stream-dismiss'),
                    behavior: HitTestBehavior.opaque,
                    onTap: () => setState(() => _editingResolution = null),
                  ),
                // 浮层不参与列表布局，展开、切组和收起都不挤动屏幕与窗口行。
                Positioned(
                  top: 0,
                  left: 16,
                  right: 16,
                  child: AnimatedSwitcher(
                    duration: const Duration(milliseconds: 180),
                    reverseDuration: const Duration(milliseconds: 120),
                    switchInCurve: Curves.easeOutCubic,
                    switchOutCurve: Curves.easeInCubic,
                    layoutBuilder: (current, previous) => Stack(
                      alignment: Alignment.topCenter,
                      children: [
                        for (final child in previous)
                          IgnorePointer(child: ExcludeSemantics(child: child)),
                        ?current,
                      ],
                    ),
                    transitionBuilder: (child, animation) => FadeTransition(
                      opacity: animation,
                      child: reduceMotion
                          ? child
                          : SlideTransition(
                              position: Tween(
                                begin: const Offset(0, -0.08),
                                end: Offset.zero,
                              ).animate(animation),
                              child: child,
                            ),
                    ),
                    child: editing == null
                        ? const SizedBox.shrink()
                        : Material(
                            key: ValueKey(editing),
                            color: c.raised,
                            shadowColor: c.shadow,
                            elevation: 4,
                            borderRadius: BorderRadius.circular(Rd.card),
                            clipBehavior: Clip.antiAlias,
                            child: ConstrainedBox(
                              constraints: BoxConstraints(
                                maxHeight: constraints.maxHeight,
                              ),
                              child: SingleChildScrollView(
                                child: _StreamOptions(
                                  value: editing ? _resolution : _fps,
                                  labels: editing
                                      ? resolutionLabels
                                      : fpsLabels,
                                  onChanged: (value) {
                                    setState(() => _editingResolution = null);
                                    if (editing) {
                                      _chooseResolution(value);
                                    } else if (value != _fps) {
                                      setState(() => _fps = value);
                                      widget.onFps!(value);
                                    }
                                  },
                                ),
                              ),
                            ),
                          ),
                  ),
                ),
                if (_error case final error?)
                  Positioned(
                    left: 16,
                    right: 16,
                    bottom: 8,
                    child: Material(
                      color: c.raised,
                      borderRadius: BorderRadius.circular(Rd.input),
                      child: Padding(
                        padding: const EdgeInsets.all(8),
                        child: Text(
                          friendlyError(error, context.l10n),
                          key: const Key('desk-resolution-error'),
                          style: TextStyle(color: c.danger),
                        ),
                      ),
                    ),
                  ),
              ],
            ),
          ),
        ),
      ],
    );
  }
}

class _StreamChoice extends StatelessWidget {
  const _StreamChoice({
    super.key,
    required this.title,
    required this.value,
    required this.values,
    required this.expanded,
    required this.onPressed,
  });

  final String title;
  final String value;
  final Iterable<String> values;
  final bool expanded;
  final VoidCallback onPressed;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final textStyle = Theme.of(context).textTheme.bodyMedium!
        .copyWith(fontSize: Fs.body, fontWeight: Fw.body);
    var valueWidth = 0.0;
    for (final label in values) {
      final painter = TextPainter(
        text: TextSpan(text: label, style: textStyle),
        textDirection: Directionality.of(context),
        textScaler: MediaQuery.textScalerOf(context),
      )..layout();
      if (painter.width > valueWidth) valueWidth = painter.width;
      painter.dispose();
    }
    return Semantics(
      expanded: expanded,
      child: TextButton(
        onPressed: onPressed,
        style: TextButton.styleFrom(
          backgroundColor: expanded ? c.tint : Colors.transparent,
          foregroundColor: expanded ? c.accent : c.text,
          padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 10),
          textStyle: textStyle,
          tapTargetSize: MaterialTapTargetSize.shrinkWrap,
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Flexible(
              child: Wrap(
                spacing: 8,
                runSpacing: 2,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  Text(
                    title,
                    style: TextStyle(color: expanded ? c.accent : c.muted),
                  ),
                  // 给最长档位留宽，选值后入口不变宽，也不会把另一入口挤到下一行。
                  SizedBox(
                    width: valueWidth,
                    child: Text(
                      value,
                      style: TextStyle(color: expanded ? c.accent : c.text),
                    ),
                  ),
                ],
              ),
            ),
            const SizedBox(width: 6),
            AnimatedRotation(
              turns: expanded ? 0.5 : 0,
              duration: MediaQuery.disableAnimationsOf(context)
                  ? Duration.zero
                  : const Duration(milliseconds: 180),
              curve: Curves.easeOutCubic,
              child: Icon(
                Icons.expand_more_rounded,
                size: Ic.small,
                color: expanded ? c.accent : c.faint,
              ),
            ),
          ],
        ),
      ),
    );
  }
}

class _StreamOptions extends StatelessWidget {
  const _StreamOptions({
    required this.value,
    required this.labels,
    required this.onChanged,
  });

  final int value;
  final Map<int, String> labels;
  final ValueChanged<int> onChanged;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final style = Theme.of(context).textTheme.bodyMedium!
        .copyWith(fontSize: Fs.body, fontWeight: Fw.body);
    return DecoratedBox(
      key: const Key('desk-stream-options'),
      decoration: BoxDecoration(
        color: c.fill,
        borderRadius: BorderRadius.circular(Rd.input),
      ),
      child: Padding(
        padding: const EdgeInsets.all(4),
        child: LayoutBuilder(
          builder: (context, constraints) {
            var widest = 0.0;
            for (final label in labels.values) {
              final text = TextPainter(
                text: TextSpan(text: label, style: style),
                textDirection: Directionality.of(context),
                textScaler: MediaQuery.textScalerOf(context),
              )..layout();
              if (text.width > widest) widest = text.width;
              text.dispose();
            }
            // 根据当前语言和字号决定列数，大字时仍保留完整档位名称。
            final available = ((constraints.maxWidth + 4) / (widest + 24 + 4))
                .floor();
            final columns = available >= labels.length
                ? labels.length
                : (available >= 2 ? 2 : 1);
            final width = (constraints.maxWidth - 4 * (columns - 1)) / columns;
            return Wrap(
              spacing: 4,
              runSpacing: 4,
              children: [
                for (final entry in labels.entries)
                  SizedBox(
                    width: width,
                    child: Semantics(
                      selected: entry.key == value,
                      child: TextButton(
                        key: Key('desk-stream-option-${entry.key}'),
                        onPressed: () => onChanged(entry.key),
                        style: TextButton.styleFrom(
                          backgroundColor: entry.key == value
                              ? c.tint
                              : Colors.transparent,
                          foregroundColor: entry.key == value
                              ? c.accent
                              : c.text,
                          textStyle: style,
                          padding: const EdgeInsets.symmetric(
                            horizontal: 12,
                            vertical: 10,
                          ),
                          shape: RoundedRectangleBorder(
                            borderRadius: BorderRadius.circular(Rd.button),
                          ),
                          tapTargetSize: MaterialTapTargetSize.shrinkWrap,
                        ),
                        child: Text(entry.value),
                      ),
                    ),
                  ),
              ],
            );
          },
        ),
      ),
    );
  }
}

String _resolution(AppLocalizations l, DeskDisplay d) => d.main
    ? l.deskResolutionMain('${d.pixelWidth}', '${d.pixelHeight}')
    : '${d.pixelWidth} × ${d.pixelHeight}';

/// 窗口所属 App 的图标；电脑没给图标时画一个通用的窗口图标。
class _AppIcon extends StatelessWidget {
  const _AppIcon(this.window, {required this.color});

  final DeskWindow window;
  final Color color;

  @override
  Widget build(BuildContext context) {
    final png = window.icon;
    if (png == null) {
      return Icon(Icons.web_asset_rounded, size: Ic.button, color: color);
    }
    return Image.memory(
      png,
      key: Key('desk-window-icon-${window.id}'),
      width: 24,
      height: 24,
      // 解出来按画的尺寸缓存，不按原图的 64 像素
      cacheWidth: (24 * MediaQuery.devicePixelRatioOf(context)).round(),
      filterQuality: FilterQuality.medium,
      gaplessPlayback: true,
      errorBuilder: (_, _, _) =>
          Icon(Icons.web_asset_rounded, size: Ic.button, color: color),
    );
  }
}

/// 窗口列表还没来、来不了或是空的时占的那一行。
class _Note extends StatelessWidget {
  const _Note(this.text, {super.key});

  final String text;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.all(16),
      child: Text(text, style: TextStyle(color: PosseColors.of(context).muted)),
    );
  }
}

/// 排布图：每块屏按相对位置和大小画成一块，正在看的那块高亮，块里写名字和分辨率。
class DeskDisplayMap extends StatelessWidget {
  const DeskDisplayMap({
    super.key,
    required this.displays,
    required this.onPick,
  });

  final DeskDisplays displays;
  final ValueChanged<int> onPick;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return LayoutBuilder(
      builder: (context, box) {
        final rects = deskDisplayLayout(displays.list, box.biggest);
        return Stack(
          children: [
            for (final d in displays.list)
              if (rects[d.id] case final r?)
                Positioned.fromRect(
                  // 屏之间留一道缝，紧挨着的两块分得清
                  rect: r.deflate(2),
                  child: GestureDetector(
                    key: Key('desk-display-map-${d.id}'),
                    onTap: () => onPick(d.id),
                    child: DecoratedBox(
                      decoration: BoxDecoration(
                        color: d.id == displays.current ? c.tint : c.wash,
                        border: Border.all(
                          color: d.id == displays.current ? c.accent : c.line,
                          width: d.id == displays.current ? 1.5 : 1,
                        ),
                        borderRadius: BorderRadius.circular(Rd.button / 2),
                      ),
                      // 小块放不下时整体缩小，不截断名字
                      child: Padding(
                        padding: const EdgeInsets.all(6),
                        child: Center(
                          child: FittedBox(
                            fit: BoxFit.scaleDown,
                            child: Column(
                              mainAxisSize: MainAxisSize.min,
                              children: [
                                Text(
                                  deskDisplayName(context.l10n, displays, d),
                                  style: TextStyle(
                                    fontSize: Fs.small,
                                    color: d.id == displays.current
                                        ? c.accent
                                        : c.text,
                                    fontWeight: Fw.medium,
                                  ),
                                ),
                                const SizedBox(height: 2),
                                Text(
                                  _resolution(context.l10n, d),
                                  style: TextStyle(
                                    fontSize: Fs.small,
                                    color: c.muted,
                                  ),
                                ),
                              ],
                            ),
                          ),
                        ),
                      ),
                    ),
                  ),
                ),
          ],
        );
      },
    );
  }
}
