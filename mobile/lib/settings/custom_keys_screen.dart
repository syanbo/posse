import 'package:flutter/material.dart';

import '../app_state.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'custom_keys.dart';

/// 设置里的「终端快捷键」：自己加的键排在快捷键栏的图片、粘贴后面，拖右边的把手排序。
class CustomKeysScreen extends StatelessWidget {
  const CustomKeysScreen({super.key, required this.model});

  final HiveAppModel model;

  void _save(List<CustomKey> keys) =>
      model.updateSettings((s) => s.customKeys = keys);

  Future<void> _edit(BuildContext context, {int? index}) async {
    final keys = model.settings.customKeys;
    final result = await showModalBottomSheet<_EditResult>(
      context: context,
      isScrollControlled: true,
      showDragHandle: true,
      useSafeArea: true,
      builder: (_) => _KeyEditor(initial: index == null ? null : keys[index]),
    );
    if (result == null) return;
    final next = List.of(model.settings.customKeys);
    if (result.key == null) {
      if (index != null) next.removeAt(index);
    } else if (index == null) {
      next.add(result.key!);
    } else {
      next[index] = result.key!;
    }
    _save(next);
  }

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: model,
      builder: (context, _) {
        final c = HiveColors.of(context);
        final keys = model.settings.customKeys;
        return Scaffold(
          appBar: AppBar(
            title: const Text('终端快捷键'),
            actions: [
              IconButton(
                tooltip: '新建快捷键',
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
                title: '自定义',
                children: [
                  if (keys.isEmpty)
                    RowTile(
                      leading: Icon(
                        Icons.add_circle_outline_rounded,
                        size: 22,
                        color: c.accent,
                      ),
                      title: const Text('新建快捷键'),
                      subtitle: const Text('一段常用的文字、Ctrl 组合键或 PgUp 这类特殊键'),
                      onTap: () => _edit(context),
                    )
                  else
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
                        onTap: () => _edit(context, index: i),
                      ),
                    ),
                ],
              ),
              Section(
                title: '内置',
                children: [
                  Padding(
                    padding: const EdgeInsets.fromLTRB(14, 12, 14, 12),
                    child: Text(
                      'Esc、Tab、Enter、⇧Tab、^C 和方向键一直在，排在自定义的后面。',
                      style: TextStyle(
                        fontSize: 12.5,
                        height: 1.5,
                        color: c.muted,
                      ),
                    ),
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
  final CustomKey item;
  final bool divider;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
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
          leading: _KeyCap(item.shown),
          title: Text(describeKey(item)),
          onTap: onTap,
          trailing: ReorderableDragStartListener(
            index: index,
            child: Padding(
              padding: const EdgeInsets.all(6),
              child: Icon(Icons.drag_handle_rounded, size: 22, color: c.faint),
            ),
          ),
        ),
      ],
    );
  }
}

/// 设置列表里说明这个键会发什么。
String describeKey(CustomKey key) => switch (key.kind) {
  'text' => key.enter ? '输入「${key.value}」并回车' : '输入「${key.value}」',
  'ctrl' => 'Ctrl + ${key.value.toUpperCase()}',
  'alt' => 'Alt + ${key.value}',
  _ => key.value,
};

/// 和快捷键栏上一样的键帽，设置里一眼对得上。
class _KeyCap extends StatelessWidget {
  const _KeyCap(this.label);

  final String label;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    return Container(
      constraints: const BoxConstraints(minWidth: 44, maxWidth: 120),
      height: 34,
      padding: const EdgeInsets.symmetric(horizontal: 12),
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: c.fill,
        borderRadius: BorderRadius.circular(Rd.button),
      ),
      child: Text(
        label,
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
        style: TextStyle(
          fontSize: 14,
          fontWeight: FontWeight.w500,
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
  static const _kindLabels = ['文字', 'Ctrl', 'Alt', '特殊键'];

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
    final c = HiveColors.of(context);
    final draft = _draft;
    final label = TextStyle(fontSize: Fs.meta, color: c.muted);
    return Padding(
      padding: EdgeInsets.only(bottom: MediaQuery.viewInsetsOf(context).bottom),
      child: SingleChildScrollView(
        padding: const EdgeInsets.fromLTRB(20, 0, 20, 20),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text(
              widget.initial == null ? '新建快捷键' : '编辑快捷键',
              style: TextStyle(
                fontSize: Fs.cardTitle,
                fontWeight: FontWeight.w600,
                color: c.text,
              ),
            ),
            const SizedBox(height: 16),
            Segmented(
              labels: _kindLabels,
              selected: CustomKey.kinds.indexOf(_kind),
              onChanged: (i) => setState(() => _kind = CustomKey.kinds[i]),
            ),
            const SizedBox(height: 16),
            ..._valueFields(c, label),
            const SizedBox(height: 16),
            Text('按钮上的字', style: label),
            const SizedBox(height: 6),
            TextField(
              controller: _label,
              onChanged: (_) => setState(() {}),
              decoration: InputDecoration(hintText: '不填就用 ${draft.shown}'),
            ),
            const SizedBox(height: 20),
            FilledButton(
              onPressed: draft.valid
                  ? () => Navigator.pop(context, _EditResult(draft))
                  : null,
              child: const Text('保存'),
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
                child: const Text('删除'),
              ),
            ],
          ],
        ),
      ),
    );
  }

  List<Widget> _valueFields(HiveColors c, TextStyle label) {
    switch (_kind) {
      case 'text':
        return [
          Text('输入的文字', style: label),
          const SizedBox(height: 6),
          TextField(
            controller: _text,
            autofocus: widget.initial == null,
            minLines: 1,
            maxLines: 4,
            style: mono(14, color: c.text),
            onChanged: (_) => setState(() {}),
            decoration: const InputDecoration(hintText: '例如 /compact'),
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
                      '输入后回车',
                      style: TextStyle(fontSize: Fs.body, color: c.text),
                    ),
                  ),
                  HiveSwitch(
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
          Text(ctrl ? 'Ctrl 加哪个键' : 'Alt 加哪个键', style: label),
          const SizedBox(height: 6),
          TextField(
            controller: _char,
            maxLength: 1,
            autocorrect: false,
            enableSuggestions: false,
            textCapitalization: TextCapitalization.none,
            style: mono(14, color: c.text),
            onChanged: (_) => setState(() {}),
            decoration: InputDecoration(
              hintText: ctrl ? '例如 r、d、z、l' : '例如 b、f、.',
              counterText: '',
              errorText: bad ? 'Ctrl 只能加字母或 @ [ \\ ] ^ _ ?' : null,
            ),
          ),
        ];
      default:
        return [
          Text('哪个键', style: label),
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
                          fontSize: 14,
                          fontWeight: FontWeight.w500,
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
