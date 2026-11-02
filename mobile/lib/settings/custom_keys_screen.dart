import 'dart:convert';

import 'package:flutter/material.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../terminal/frames.dart';
import 'custom_keys.dart';

/// 设置里的「终端快捷键」：快捷键栏上除键盘开关以外的键都在一张表里，拖右边的把手排序；
/// 内置的不能改也不能删，自己加的点开修改。
class CustomKeysScreen extends StatelessWidget {
  const CustomKeysScreen({super.key, required this.model});

  final PosseAppModel model;

  void _save(List<BarKey> keys) =>
      model.updateSettings((s) => s.barKeys = keys);

  Future<void> _edit(BuildContext context, {int? index}) async {
    final initial = index == null ? null : model.settings.barKeys[index].custom;
    final result = await showModalBottomSheet<_EditResult>(
      context: context,
      isScrollControlled: true,
      showDragHandle: true,
      useSafeArea: true,
      builder: (_) => _KeyEditor(initial: initial),
    );
    if (result == null) return;
    final next = List.of(model.settings.barKeys);
    if (result.key == null) {
      if (index != null) next.removeAt(index);
    } else if (index == null) {
      next.insert(_insertAt(next), BarKey.custom(result.key!));
    } else {
      next[index] = BarKey.custom(result.key!);
    }
    _save(next);
  }

  /// 新键接在自己加的最后一个后面；还没有时放在第一个终端键前面，和默认顺序一致。
  static int _insertAt(List<BarKey> keys) {
    final last = keys.lastIndexWhere((key) => key.custom != null);
    if (last >= 0) return last + 1;
    final term = keys.indexWhere((key) => key.termKey != null);
    return term >= 0 ? term : keys.length;
  }

  static String _order(List<BarKey> keys) =>
      jsonEncode([for (final key in keys) key.toJson()]);

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) {
        final c = PosseColors.of(context);
        final l = context.l10n;
        final keys = model.settings.barKeys;
        final reset = defaultBarKeys(model.settings.customKeys);
        return Scaffold(
          appBar: AppBar(
            title: Text(l.keysTitle),
            actions: [
              IconButton(
                tooltip: l.keysNew,
                onPressed: () => _edit(context),
                icon: const Icon(Icons.add_rounded),
              ),
              const SizedBox(width: 4),
            ],
          ),
          body: ListView(
            padding: const EdgeInsets.only(top: 8, bottom: 32),
            children: [
              Section(
                title: l.keysBar,
                children: [
                  ReorderableListView.builder(
                    shrinkWrap: true,
                    physics: const NeverScrollableScrollPhysics(),
                    buildDefaultDragHandles: false,
                    itemCount: keys.length,
                    proxyDecorator: (child, _, _) => Material(
                      color: c.raised,
                      elevation: 6,
                      borderRadius: BorderRadius.circular(Rd.card),
                      child: child,
                    ),
                    onReorderItem: (from, to) {
                      final next = List.of(keys);
                      next.insert(to, next.removeAt(from));
                      _save(next);
                    },
                    itemBuilder: (context, i) => _KeyRow(
                      key: ValueKey(i),
                      index: i,
                      item: keys[i],
                      divider: i > 0,
                      onTap: keys[i].custom == null
                          ? null
                          : () => _edit(context, index: i),
                    ),
                  ),
                ],
              ),
              Padding(
                padding: const EdgeInsets.fromLTRB(20, 0, 20, 12),
                child: Text(
                  l.keysBarNote,
                  style: TextStyle(
                    fontSize: Fs.small,
                    height: Lh.body,
                    color: c.muted,
                  ),
                ),
              ),
              Section(
                children: [
                  RowTile(
                    chevron: false,
                    leading: Icon(
                      Icons.add_circle_outline_rounded,
                      size: Ic.button,
                      color: c.accent,
                    ),
                    title: Text(l.keysNew),
                    subtitle: Text(l.keysNewNote),
                    onTap: () => _edit(context),
                  ),
                  if (_order(keys) != _order(reset))
                    RowTile(
                      chevron: false,
                      leading: Icon(
                        Icons.restart_alt_rounded,
                        size: Ic.button,
                        color: c.accent,
                      ),
                      title: Text(l.keysResetOrder),
                      subtitle: Text(l.keysResetOrderNote),
                      onTap: () => _save(reset),
                    ),
                ],
              ),
            ],
          ),
        );
      },
    );
  }
}

class _KeyRow extends StatelessWidget {
  const _KeyRow({
    super.key,
    required this.index,
    required this.item,
    required this.divider,
    required this.onTap,
  });

  final int index;
  final BarKey item;
  final bool divider;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final custom = item.custom;
    return Column(
      mainAxisSize: MainAxisSize.min,
      children: [
        if (divider)
          Padding(
            padding: const EdgeInsets.only(left: 16),
            child: Divider(height: 1, thickness: 1, color: c.line),
          ),
        RowTile(
          chevron: false,
          leading: switch (item.builtin) {
            'image' => const _KeyCap(icon: Icons.attach_file_rounded),
            'paste' => const _KeyCap(icon: Icons.content_paste_rounded),
            sendNowLabel => _KeyCap(glyph: SendNowGlyph(color: c.text)),
            _ => _KeyCap(
              label: custom?.shown ?? item.termKey?.label(l) ?? item.builtin,
            ),
          },
          title: Text(
            custom != null
                ? describeKey(l, custom)
                : describeBuiltin(l, item.builtin!),
            // 英文的说明比中文长，折行写全
            maxLines: 3,
          ),
          subtitle: custom == null ? Text(l.keysBuiltin) : null,
          onTap: onTap,
          trailing: ReorderableDragStartListener(
            index: index,
            child: Padding(
              padding: const EdgeInsets.all(6),
              child: Icon(
                Icons.drag_handle_rounded,
                size: Ic.button,
                color: c.faint,
              ),
            ),
          ),
        ),
      ],
    );
  }
}

/// 设置列表里说明这个键会发什么。
String describeKey(AppLocalizations l, CustomKey key) => switch (key.kind) {
  'text' =>
    key.enter ? l.keysTypeTextEnter(key.value) : l.keysTypeText(key.value),
  'ctrl' => 'Ctrl + ${key.value.toUpperCase()}',
  'alt' => 'Alt + ${key.value}',
  _ => key.value,
};

/// 内置键是做什么的。
String describeBuiltin(AppLocalizations l, String name) => switch (name) {
  'image' => l.keysImage,
  'paste' => l.keysPaste,
  'Esc' => l.keysEsc,
  'Tab' => l.keysTab,
  'Enter' => l.keysEnter,
  sendNowLabel => l.keysSendNow,
  '⇧Tab' => l.keysShiftTab,
  '^C' => l.keysCtrlC,
  clearInputKey => l.keysClear,
  exitAgentLabel => l.keysExit,
  '↑' => l.keysUp,
  '↓' => l.keysDown,
  '←' => l.keysLeft,
  '→' => l.keysRight,
  _ => name,
};

/// 和快捷键栏上一样的键帽，设置里一眼对得上。
class _KeyCap extends StatelessWidget {
  const _KeyCap({this.label, this.icon, this.glyph});

  final String? label;
  final IconData? icon;
  final Widget? glyph;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Container(
      constraints: const BoxConstraints(minWidth: 44, maxWidth: 120),
      height: 34,
      padding: const EdgeInsets.symmetric(horizontal: 12),
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: c.fill,
        borderRadius: BorderRadius.circular(Rd.button),
      ),
      child: glyph != null
          ? glyph!
          : icon != null
          ? Icon(icon, size: Ic.medium, color: c.text)
          : Text(
              label ?? '',
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(
                fontSize: Fs.body,
                fontWeight: Fw.medium,
                color: c.text,
              ),
            ),
    );
  }
}

/// 编辑面板的结果：key 为空表示删除。
class _EditResult {
  const _EditResult(this.key);
  final CustomKey? key;
}

class _KeyEditor extends StatefulWidget {
  const _KeyEditor({required this.initial});

  final CustomKey? initial;

  @override
  State<_KeyEditor> createState() => _KeyEditorState();
}

class _KeyEditorState extends State<_KeyEditor> {
  late String _kind = widget.initial?.kind ?? 'text';
  late final _label = TextEditingController(text: widget.initial?.label);
  late final _text = TextEditingController(
    text: widget.initial?.kind == 'text' ? widget.initial!.value : '',
  );
  late final _char = TextEditingController(
    text: {'ctrl', 'alt'}.contains(widget.initial?.kind)
        ? widget.initial!.value
        : '',
  );
  late String _special = widget.initial?.kind == 'special'
      ? widget.initial!.value
      : specialKeys.keys.first;
  late bool _enter = widget.initial?.enter ?? false;

  @override
  void dispose() {
    _label.dispose();
    _text.dispose();
    _char.dispose();
    super.dispose();
  }

  CustomKey get _draft => CustomKey(
    kind: _kind,
    label: _label.text,
    value: switch (_kind) {
      'text' => _text.text,
      'special' => _special,
      _ => _char.text,
    },
    enter: _kind == 'text' && _enter,
  );

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final draft = _draft;
    final label = TextStyle(fontSize: Fs.small, color: c.muted);
    return Padding(
      padding: EdgeInsets.only(bottom: MediaQuery.viewInsetsOf(context).bottom),
      child: SingleChildScrollView(
        padding: const EdgeInsets.fromLTRB(20, 0, 20, 20),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text(
              widget.initial == null ? l.keysNew : l.keysEdit,
              style: TextStyle(
                fontSize: Fs.title,
                fontWeight: Fw.bold,
                color: c.text,
              ),
            ),
            const SizedBox(height: 16),
            Segmented(
              labels: [l.keysKindText, 'Ctrl', 'Alt', l.keysKindSpecial],
              selected: CustomKey.kinds.indexOf(_kind),
              onChanged: (i) => setState(() => _kind = CustomKey.kinds[i]),
            ),
            const SizedBox(height: 16),
            ..._valueFields(l, c, label),
            const SizedBox(height: 16),
            Text(l.keysLabel, style: label),
            const SizedBox(height: 6),
            TextField(
              controller: _label,
              onChanged: (_) => setState(() {}),
              decoration: InputDecoration(
                hintText: l.keysLabelHint(draft.shown),
              ),
            ),
            const SizedBox(height: 20),
            FilledButton(
              onPressed: draft.valid
                  ? () => Navigator.pop(context, _EditResult(draft))
                  : null,
              child: Text(l.commonSave),
            ),
            if (widget.initial != null) ...[
              const SizedBox(height: 8),
              TextButton(
                style: TextButton.styleFrom(
                  backgroundColor: Colors.transparent,
                  foregroundColor: c.danger,
                ),
                onPressed: () =>
                    Navigator.pop(context, const _EditResult(null)),
                child: Text(l.commonDelete),
              ),
            ],
          ],
        ),
      ),
    );
  }

  List<Widget> _valueFields(
    AppLocalizations l,
    PosseColors c,
    TextStyle label,
  ) {
    switch (_kind) {
      case 'text':
        return [
          Text(l.keysText, style: label),
          const SizedBox(height: 6),
          TextField(
            controller: _text,
            autofocus: widget.initial == null,
            minLines: 1,
            maxLines: 4,
            style: mono(Fs.body, color: c.text),
            onChanged: (_) => setState(() {}),
            decoration: InputDecoration(hintText: l.keysTextHint),
          ),
          const SizedBox(height: 4),
          InkWell(
            onTap: () => setState(() => _enter = !_enter),
            child: Padding(
              padding: const EdgeInsets.symmetric(vertical: 6),
              child: Row(
                children: [
                  Expanded(
                    child: Text(
                      l.keysEnterAfter,
                      style: TextStyle(fontSize: Fs.body, color: c.text),
                    ),
                  ),
                  PosseSwitch(
                    value: _enter,
                    onChanged: (v) => setState(() => _enter = v),
                  ),
                ],
              ),
            ),
          ),
        ];
      case 'ctrl' || 'alt':
        final ctrl = _kind == 'ctrl';
        final bad = _char.text.isNotEmpty && !_draft.valid;
        return [
          Text(ctrl ? l.keysCtrlWhich : l.keysAltWhich, style: label),
          const SizedBox(height: 6),
          TextField(
            controller: _char,
            maxLength: 1,
            autocorrect: false,
            enableSuggestions: false,
            textCapitalization: TextCapitalization.none,
            style: mono(Fs.body, color: c.text),
            onChanged: (_) => setState(() {}),
            decoration: InputDecoration(
              hintText: ctrl ? l.keysCtrlHint : l.keysAltHint,
              counterText: '',
              errorText: bad ? l.keysCtrlInvalid : null,
            ),
          ),
        ];
      default:
        return [
          Text(l.keysSpecialWhich, style: label),
          const SizedBox(height: 8),
          Wrap(
            spacing: 6,
            runSpacing: 6,
            children: [
              for (final name in specialKeys.keys)
                // 选中照全局约定是强调色淡底
                Material(
                  color: _special == name ? c.tint : c.fill,
                  borderRadius: BorderRadius.circular(Rd.button),
                  child: InkWell(
                    borderRadius: BorderRadius.circular(Rd.button),
                    onTap: () => setState(() => _special = name),
                    child: Container(
                      constraints: const BoxConstraints(minWidth: 52),
                      height: 36,
                      padding: const EdgeInsets.symmetric(horizontal: 12),
                      alignment: Alignment.center,
                      child: Text(
                        name,
                        style: TextStyle(
                          fontSize: Fs.body,
                          fontWeight: Fw.medium,
                          color: _special == name ? c.accent : c.text,
                        ),
                      ),
                    ),
                  ),
                ),
            ],
          ),
        ];
    }
  }
}
