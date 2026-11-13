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
            child: SingleChildScrollView(
              child: Column(
                children: [
                  if (resolution != null && onResolution != null)
                    _ResolutionChoice(
                      value: resolution,
                      onChanged: onResolution,
                    ),
                  if (fps != null && onFps != null)
                    _FpsChoice(fps: fps, onChanged: onFps),
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
        );
      },
    );
  } finally {
    generation?.removeListener(invalidate);
    target?.removeListener(invalidate);
  }
}

class _ResolutionChoice extends StatefulWidget {
  const _ResolutionChoice({required this.value, required this.onChanged});

  final int value;
  final Future<void> Function(int) onChanged;

  @override
  State<_ResolutionChoice> createState() => _ResolutionChoiceState();
}

class _ResolutionChoiceState extends State<_ResolutionChoice> {
  late int _value = widget.value;
  Object? _error;
  int _generation = 0;

  Future<void> _choose(int value) async {
    if (value == _value && _error == null) return;
    final gen = ++_generation;
    setState(() {
      _value = value;
      _error = null;
    });
    try {
      await widget.onChanged(value);
    } on Object catch (error) {
      // 旧请求的迟到错误不覆盖后来的选择；偏好仍保留，下次订阅重试。
      if (mounted && gen == _generation) setState(() => _error = error);
    }
  }

  @override
  Widget build(BuildContext context) {
    const values = [0, ...deskResolutionChoices];
    return Section(
      title: context.l10n.deskQuality,
      children: [
        Padding(
          padding: const EdgeInsets.all(10),
          child: Segmented(
            key: const Key('desk-resolution'),
            labels: [
              context.l10n.deskQualityAuto,
              for (final v in deskResolutionChoices) '${v}p',
            ],
            selected: values.indexOf(_value).clamp(0, values.length - 1),
            onChanged: (i) => _choose(values[i]),
          ),
        ),
        if (_error case final error?)
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 0, 16, 12),
            child: Text(
              friendlyError(error, context.l10n),
              key: const Key('desk-resolution-error'),
              style: TextStyle(color: PosseColors.of(context).danger),
            ),
          ),
      ],
    );
  }
}

/// 帧率一行：自动和 [deskFpsChoices] 几档，选中的跟着点击换，不等电脑回。
class _FpsChoice extends StatefulWidget {
  const _FpsChoice({required this.fps, required this.onChanged});

  final int fps;
  final ValueChanged<int> onChanged;

  @override
  State<_FpsChoice> createState() => _FpsChoiceState();
}

class _FpsChoiceState extends State<_FpsChoice> {
  late int _fps = widget.fps;

  @override
  Widget build(BuildContext context) {
    const values = [0, ...deskFpsChoices];
    return Section(
      title: context.l10n.deskFrameRate,
      children: [
        Padding(
          padding: const EdgeInsets.all(10),
          child: Segmented(
            key: const Key('desk-fps'),
            labels: [
              context.l10n.deskFrameRateAuto,
              for (final v in deskFpsChoices) '$v',
            ],
            selected: values.indexOf(_fps).clamp(0, values.length - 1),
            onChanged: (i) {
              if (values[i] == _fps) return;
              setState(() => _fps = values[i]);
              widget.onChanged(values[i]);
            },
          ),
        ),
      ],
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
