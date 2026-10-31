import 'dart:async';

import 'package:flutter/material.dart';

import '../ui/theme.dart';
import 'chrome.dart';
import 'mods.dart';

/// 和终端快捷键栏同一排的高度：40 的按键加 1 的上边线。安全区由外面补。
const deskKeyBarBody = 40.0;

/// 点按和长按的分界，和画面上的长按拖动一样。
const deskModHold = Duration(milliseconds: 300);

class DeskKeyBar extends StatelessWidget {
  const DeskKeyBar({
    super.key,
    required this.mods,
    required this.keyboardUp,
    required this.onKeyboard,
    required this.onKey,
    required this.onModTap,
    required this.onModLock,
    this.dense = false,
  });

  final DeskMods mods;
  final bool keyboardUp;
  final VoidCallback onKeyboard;
  final void Function(String key) onKey;
  final void Function(DeskMod mod) onModTap;
  final void Function(DeskMod mod) onModLock;

  /// 横屏高度紧：更矮，底色半透明，让画面从键下面透出来。
  final bool dense;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    const keys = ['Esc', 'Tab', '↑', '↓', '←', '→', '◀ 空间', '空间 ▶', '调度中心'];
    final color = dense ? c.band.withValues(alpha: 0.86) : c.band;
    return Container(
      decoration: BoxDecoration(
        color: color,
        border: Border(top: BorderSide(color: c.line)),
      ),
      child: SafeArea(
        top: false,
        child: SizedBox(
          height: dense ? deskKeyBarDense : deskKeyBarBody,
          child: _DeskKeyStrip(
            color: color,
            dense: dense,
            children: [
              _keyboard(c),
              for (final label in keys) _textKey(c, label, () => onKey(label)),
              for (final mod in DeskMod.values)
                _ModKey(
                  label: deskModMark[mod]!,
                  mode: mods.mode(mod),
                  onTap: () => onModTap(mod),
                  onLock: () => onModLock(mod),
                ),
            ],
          ),
        ),
      ),
    );
  }

  Widget _keyboard(PosseColors c) {
    return Material(
      color: c.fill,
      borderRadius: BorderRadius.circular(Rd.button),
      child: InkWell(
        borderRadius: BorderRadius.circular(Rd.button),
        onTap: onKeyboard,
        child: SizedBox(
          width: 40,
          child: Icon(
            keyboardUp ? Icons.keyboard_hide_rounded : Icons.keyboard_rounded,
            size: Ic.button,
            color: c.text,
          ),
        ),
      ),
    );
  }

  Widget _textKey(PosseColors c, String label, VoidCallback onTap) {
    return Material(
      color: c.fill,
      borderRadius: BorderRadius.circular(Rd.button),
      child: InkWell(
        borderRadius: BorderRadius.circular(Rd.button),
        onTap: onTap,
        child: Container(
          constraints: const BoxConstraints(minWidth: 40),
          padding: const EdgeInsets.symmetric(horizontal: 10),
          alignment: Alignment.center,
          child: Text(
            label,
            maxLines: 1,
            style: TextStyle(
              fontSize: Fs.small,
              fontWeight: Fw.medium,
              color: c.text,
            ),
          ),
        ),
      ),
    );
  }
}

/// 竖屏一屏放不下时，右侧渐隐表示还能往左滑。滑到头就去掉，最后一颗键要看全。
class _DeskKeyStrip extends StatefulWidget {
  const _DeskKeyStrip({
    required this.color,
    required this.dense,
    required this.children,
  });

  final Color color;
  final bool dense;
  final List<Widget> children;

  @override
  State<_DeskKeyStrip> createState() => _DeskKeyStripState();
}

class _DeskKeyStripState extends State<_DeskKeyStrip> {
  var _more = false;

  void _note(ScrollMetrics metrics) {
    if (metrics.axis != Axis.horizontal) return;
    final more = metrics.extentAfter > 1;
    if (more == _more) return;
    setState(() => _more = more);
  }

  @override
  Widget build(BuildContext context) {
    return NotificationListener<Notification>(
      onNotification: (notification) {
        final metrics = switch (notification) {
          ScrollMetricsNotification(:final metrics) => metrics,
          ScrollNotification(:final metrics) => metrics,
          _ => null,
        };
        if (metrics != null) _note(metrics);
        return false;
      },
      child: Stack(
        children: [
          ListView.separated(
            scrollDirection: Axis.horizontal,
            padding: EdgeInsets.symmetric(
              horizontal: 8,
              vertical: widget.dense ? 2 : 4,
            ),
            itemCount: widget.children.length,
            separatorBuilder: (_, _) => const SizedBox(width: 6),
            itemBuilder: (context, i) => widget.children[i],
          ),
          if (_more)
            Positioned(
              key: const Key('desk-key-fade'),
              right: 0,
              top: 0,
              bottom: 0,
              width: 40,
              child: IgnorePointer(
                child: DecoratedBox(
                  decoration: BoxDecoration(
                    gradient: LinearGradient(
                      colors: [widget.color.withValues(alpha: 0), widget.color],
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

class _ModKey extends StatefulWidget {
  const _ModKey({
    required this.label,
    required this.mode,
    required this.onTap,
    required this.onLock,
  });

  final String label;
  final DeskModMode mode;
  final VoidCallback onTap;
  final VoidCallback onLock;

  @override
  State<_ModKey> createState() => _ModKeyState();
}

class _ModKeyState extends State<_ModKey> {
  Timer? _hold;
  var _locked = false;

  @override
  void dispose() {
    _hold?.cancel();
    super.dispose();
  }

  void _down(TapDownDetails _) {
    _locked = false;
    _hold?.cancel();
    _hold = Timer(deskModHold, () {
      _locked = true;
      widget.onLock();
    });
  }

  void _up(TapUpDetails _) {
    final pending = _hold?.isActive ?? false;
    _hold?.cancel();
    if (pending && !_locked) widget.onTap();
  }

  void _cancel() {
    _hold?.cancel();
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final locked = widget.mode == DeskModMode.locked;
    final on = widget.mode != DeskModMode.off;
    final fill = locked ? c.accent : (on ? c.tint : c.fill);
    final ink = locked ? Colors.white : (on ? c.accent : c.text);
    return Material(
      color: fill,
      borderRadius: BorderRadius.circular(Rd.button),
      child: InkWell(
        borderRadius: BorderRadius.circular(Rd.button),
        onTapDown: _down,
        onTapUp: _up,
        onTapCancel: _cancel,
        child: Container(
          constraints: const BoxConstraints(minWidth: 40),
          padding: const EdgeInsets.symmetric(horizontal: 10),
          alignment: Alignment.center,
          child: Text(
            widget.label,
            maxLines: 1,
            style: TextStyle(
              fontSize: Fs.title,
              fontWeight: Fw.medium,
              color: ink,
            ),
          ),
        ),
      ),
    );
  }
}
