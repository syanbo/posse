import 'dart:typed_data';

import 'package:flutter/material.dart';

import '../ui/loading.dart';
import '../ui/theme.dart';

/// 对话页底部的输入区：待发的图片、选图按钮、输入框和发送按钮。状态都在对话页里，这里只画。
class ChatComposer extends StatelessWidget {
  const ChatComposer({
    super.key,
    required this.controller,
    required this.images,
    required this.sending,
    required this.connected,
    required this.ended,
    required this.hint,
    required this.onChanged,
    required this.onSubmit,
    required this.onPickImage,
    required this.onRemoveImage,
    required this.onPaste,
    required this.onContentInserted,
  });

  final TextEditingController controller;
  final List<Uint8List> images;
  final bool sending;
  final bool connected;

  /// 会话已经结束：不能再输入，换成一行说明。
  final bool ended;
  final String hint;
  final ValueChanged<String> onChanged;
  final VoidCallback onSubmit;
  final VoidCallback onPickImage;
  final ValueChanged<int> onRemoveImage;

  /// 菜单里的粘贴：只认文字和图片，由对话页读剪贴板。
  final VoidCallback onPaste;
  final ValueChanged<KeyboardInsertedContent> onContentInserted;

  void _pasteFromMenu(EditableTextState state) {
    state.hideToolbar();
    onPaste();
  }

  /// 系统菜单和 Flutter 菜单的粘贴都走同一条：只认文字和图片。
  Widget _composerMenu(BuildContext context, EditableTextState state) {
    if (SystemContextMenu.isSupported(context)) {
      final label = WidgetsLocalizations.of(context).pasteButtonLabel;
      final items = <IOSSystemContextMenuItem>[];
      var sawPaste = false;
      for (final item in SystemContextMenu.getDefaultItems(state)) {
        if (item is IOSSystemContextMenuItemPaste) {
          sawPaste = true;
          items.add(
            IOSSystemContextMenuItemCustom(
              title: label,
              onPressed: () => _pasteFromMenu(state),
            ),
          );
        } else {
          items.add(item);
        }
      }
      if (!sawPaste) {
        items.add(
          IOSSystemContextMenuItemCustom(
            title: label,
            onPressed: () => _pasteFromMenu(state),
          ),
        );
      }
      return SystemContextMenu.editableText(
        editableTextState: state,
        items: items,
      );
    }
    final items = state.contextMenuButtonItems.map((item) {
      if (item.type != ContextMenuButtonType.paste) return item;
      return item.copyWith(onPressed: () => _pasteFromMenu(state));
    }).toList();
    if (!items.any((item) => item.type == ContextMenuButtonType.paste)) {
      items.add(
        ContextMenuButtonItem(
          type: ContextMenuButtonType.paste,
          onPressed: () => _pasteFromMenu(state),
        ),
      );
    }
    return AdaptiveTextSelectionToolbar.buttonItems(
      anchors: state.contextMenuAnchors,
      buttonItems: items,
    );
  }

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    if (ended) {
      return SafeArea(
        top: false,
        child: Container(
          width: double.infinity,
          margin: const EdgeInsets.fromLTRB(12, 4, 12, 8),
          padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 14),
          decoration: BoxDecoration(
            color: c.fill,
            borderRadius: BorderRadius.circular(Rd.composer),
          ),
          child: Text(
            '这个会话已经结束，记录还在。要继续请在首页新建会话，或者在电脑的历史页恢复。',
            style: TextStyle(fontSize: 13, height: 1.5, color: c.muted),
          ),
        ),
      );
    }
    final ready =
        !sending &&
        connected &&
        (images.isNotEmpty || controller.text.trim().isNotEmpty);
    return SafeArea(
      top: false,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(10, 2, 10, 6),
        child: Container(
          decoration: BoxDecoration(
            color: c.card,
            borderRadius: BorderRadius.circular(Rd.composer),
            border: Border.all(color: c.line),
          ),
          // 四边一样宽：单行时按钮正好在框的正中，多行时停在底部
          padding: const EdgeInsets.all(4),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              if (images.isNotEmpty)
                SizedBox(
                  height: 56,
                  child: ListView.separated(
                    scrollDirection: Axis.horizontal,
                    padding: const EdgeInsets.fromLTRB(6, 4, 6, 4),
                    itemCount: images.length,
                    separatorBuilder: (_, _) => const SizedBox(width: 8),
                    itemBuilder: (context, index) {
                      return SizedBox(
                        width: 48,
                        height: 48,
                        child: Stack(
                          children: [
                            ClipRRect(
                              borderRadius: BorderRadius.circular(Rd.button),
                              child: Image.memory(
                                images[index],
                                width: 48,
                                height: 48,
                                fit: BoxFit.cover,
                              ),
                            ),
                            Positioned(
                              top: 0,
                              right: 0,
                              child: Tooltip(
                                message: '不发这张',
                                child: GestureDetector(
                                  onTap: sending
                                      ? null
                                      : () => onRemoveImage(index),
                                  child: const DecoratedBox(
                                    decoration: BoxDecoration(
                                      color: Color(0xCC1A1A19),
                                      borderRadius: BorderRadius.only(
                                        topRight: Radius.circular(Rd.button),
                                        bottomLeft: Radius.circular(Rd.button),
                                      ),
                                    ),
                                    child: SizedBox(
                                      width: 18,
                                      height: 18,
                                      child: Icon(
                                        Icons.close_rounded,
                                        size: 14,
                                        color: Colors.white,
                                      ),
                                    ),
                                  ),
                                ),
                              ),
                            ),
                          ],
                        ),
                      );
                    },
                  ),
                ),
              Row(
                // 发送按钮和输入框上下居中
                crossAxisAlignment: CrossAxisAlignment.center,
                children: [
                  IconButton(
                    tooltip: '发送图片',
                    // Material 会把点按区补到 48、撑高整条，收到和发送按钮一样 34
                    style: IconButton.styleFrom(
                      fixedSize: const Size.square(34),
                      minimumSize: const Size.square(34),
                      padding: EdgeInsets.zero,
                      tapTargetSize: MaterialTapTargetSize.shrinkWrap,
                    ),
                    onPressed: sending || !connected ? null : onPickImage,
                    icon: Icon(
                      Icons.image_outlined,
                      size: 22,
                      color: images.isEmpty ? c.muted : c.accent,
                    ),
                  ),
                  Expanded(
                    child: TextField(
                      controller: controller,
                      minLines: 1,
                      maxLines: 6,
                      textInputAction: TextInputAction.newline,
                      onChanged: onChanged,
                      contentInsertionConfiguration:
                          ContentInsertionConfiguration(
                            onContentInserted: onContentInserted,
                          ),
                      contextMenuBuilder: _composerMenu,
                      style: TextStyle(fontSize: Fs.body, color: c.text),
                      decoration: InputDecoration(
                        hintText: hint,
                        filled: false,
                        border: InputBorder.none,
                        enabledBorder: InputBorder.none,
                        focusedBorder: InputBorder.none,
                        isDense: true,
                        // 单行时和 34 高的发送按钮一样高
                        contentPadding: const EdgeInsets.fromLTRB(6, 5, 4, 6),
                      ),
                    ),
                  ),
                  AnimatedContainer(
                    duration: const Duration(milliseconds: 150),
                    width: 34,
                    height: 34,
                    decoration: BoxDecoration(
                      color: ready ? c.accent : c.fill,
                      shape: BoxShape.circle,
                    ),
                    child: IconButton(
                      padding: EdgeInsets.zero,
                      tooltip: '发送',
                      onPressed: ready ? onSubmit : null,
                      icon: sending
                          ? HexSpinner(color: c.muted)
                          : Icon(
                              Icons.arrow_upward_rounded,
                              size: 20,
                              color: ready ? Colors.white : c.faint,
                            ),
                    ),
                  ),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }
}
