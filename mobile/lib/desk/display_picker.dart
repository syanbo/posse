import 'package:flutter/material.dart';

import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'displays.dart';

/// 选好的画面：整块屏或一个窗口，[id] 是屏的 ID 或窗口的 ID。
typedef DeskPick = ({bool window, int id});

/// 选看什么：上面是屏（多块时按真实排布画出各块，点哪块切哪块），下面是当前空间里的窗口，
/// [windows] 打开时现要。返回选中的，取消为 null。
Future<DeskPick?> pickDeskView(
  BuildContext context,
  DeskDisplays displays, {
  required Future<List<DeskWindow>> windows,
}) {
  return showModalBottomSheet<DeskPick>(
    context: context,
    showDragHandle: true,
    // 看桌面常是横屏，默认的高度上限放不下排布图加列表
    isScrollControlled: true,
    builder: (sheet) {
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
                Section(
                  title: '整块屏',
                  inset: 48,
                  children: [
                    if (displays.several)
                      Padding(
                        padding: const EdgeInsets.fromLTRB(16, 12, 16, 12),
                        child: SizedBox(
                          height: 120,
                          child: DeskDisplayMap(
                            displays: displays,
                            onPick: (id) => pick((window: false, id: id)),
                          ),
                        ),
                      ),
                    for (final (i, d) in displays.list.indexed)
                      RowTile(
                        key: Key('desk-display-${d.id}'),
                        leading: SizedBox(
                          width: 22,
                          child: Text(
                            '${i + 1}',
                            textAlign: TextAlign.center,
                            style: TextStyle(
                              color: d.id == displays.current
                                  ? c.accent
                                  : c.muted,
                              fontWeight: FontWeight.w600,
                            ),
                          ),
                        ),
                        title: Text(deskDisplayName(displays, d)),
                        subtitle: Text(
                          '${d.pixelWidth} × ${d.pixelHeight}${d.main ? ' · 主屏' : ''}',
                        ),
                        trailing: watching == null && d.id == displays.current
                            ? Icon(
                                Icons.check_rounded,
                                size: 20,
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
                      title: '窗口',
                      inset: 48,
                      children: [
                        if (snap.connectionState != ConnectionState.done)
                          const _Note(
                            key: Key('desk-windows-loading'),
                            '正在列出窗口…',
                          )
                        else if (snap.hasError)
                          const _Note('列不出窗口，稍后再试')
                        else if (list == null || list.isEmpty)
                          const _Note('这个空间里没有可以单独看的窗口')
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
                                      size: 20,
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
}

/// 窗口所属 App 的图标；电脑没给图标时画一个通用的窗口图标。
class _AppIcon extends StatelessWidget {
  const _AppIcon(this.window, {required this.color});

  final DeskWindow window;
  final Color color;

  @override
  Widget build(BuildContext context) {
    final png = window.icon;
    if (png == null) {
      return Icon(Icons.web_asset_rounded, size: 22, color: color);
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
          Icon(Icons.web_asset_rounded, size: 22, color: color),
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
      padding: const EdgeInsets.fromLTRB(16, 18, 16, 18),
      child: Text(text, style: TextStyle(color: PosseColors.of(context).muted)),
    );
  }
}

/// 排布图：每块屏按相对位置和大小画成一块，正在看的那块高亮，块上写序号（和下面列表的顺序一样）。
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
            for (final (i, d) in displays.list.indexed)
              if (rects[d.id] case final r?)
                Positioned.fromRect(
                  // 屏之间留一道缝，紧挨着的两块分得清
                  rect: r.deflate(2),
                  child: GestureDetector(
                    key: Key('desk-display-map-${d.id}'),
                    onTap: () => onPick(d.id),
                    child: DecoratedBox(
                      decoration: BoxDecoration(
                        color: d.id == displays.current
                            ? c.accent.withValues(alpha: 0.22)
                            : c.wash,
                        border: Border.all(
                          color: d.id == displays.current ? c.accent : c.line,
                          width: d.id == displays.current ? 1.5 : 1,
                        ),
                        borderRadius: BorderRadius.circular(Rd.button / 2),
                      ),
                      child: Center(
                        child: Text(
                          '${i + 1}',
                          style: TextStyle(
                            color: d.id == displays.current
                                ? c.accent
                                : c.muted,
                            fontWeight: FontWeight.w600,
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
