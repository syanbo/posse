import 'dart:convert';

import 'package:flutter/material.dart';

import '../l10n/l10n.dart';
import 'markdown_view.dart';
import 'models.dart';
import 'transcript.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import 'chat_style.dart';
import 'echo.dart';
import '../ui/widgets.dart';

/// 自己的话左边至少空出的宽度，和助理正文错开。
const _bubbleIndent = 48.0;

/// 自己的话：靠右、强调色淡底。
Widget _mine(BuildContext context, Widget body) {
  return Align(
    alignment: Alignment.centerRight,
    child: Container(
      constraints: BoxConstraints(
        maxWidth: MediaQuery.sizeOf(context).width * 0.8,
      ),
      margin: const EdgeInsets.fromLTRB(_bubbleIndent, 4, 12, 10),
      padding: const EdgeInsets.fromLTRB(16, 10, 16, 12),
      decoration: BoxDecoration(
        color: chatTint(context),
        borderRadius: BorderRadius.circular(Rd.input),
      ),
      child: body,
    ),
  );
}

/// 刚发出去、agent 还没写进会话文件的话。样子和自己的话一样，时间处写发送中或已发送。
/// 倒序列表离视觉顶部不到这么远就读更早的一页。
const earlierThreshold = 120.0;

/// 倒序列表（偏移 0 是底部）是否已经翻到靠近最早的那头。
bool nearEarliest(ScrollPosition position) =>
    position.maxScrollExtent - position.pixels <= earlierThreshold;

/// 列表最上面那一行：翻到这里会自动读更早的消息，读着时转圈；没读成（断线）时留个按钮手动再试。
class EarlierRow extends StatelessWidget {
  const EarlierRow({super.key, required this.loading, required this.onLoad});

  final bool loading;
  final VoidCallback onLoad;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return SizedBox(
      height: 44,
      child: Center(
        child: loading
            ? const HexSpinner(size: Ic.medium)
            : TextButton(
                style: TextButton.styleFrom(
                  minimumSize: const Size(0, 30),
                  padding: const EdgeInsets.symmetric(horizontal: 12),
                  backgroundColor: Colors.transparent,
                  foregroundColor: c.muted,
                  textStyle: const TextStyle(fontSize: Fs.small),
                ),
                onPressed: onLoad,
                child: Text(context.l10n.chatEarlierMessages),
              ),
      ),
    );
  }
}

class EchoBubble extends StatelessWidget {
  const EchoBubble({super.key, required this.echo});

  final LocalEcho echo;

  @override
  Widget build(BuildContext context) {
    final muted = chatMuted(context);
    return Opacity(
      opacity: echo.sent ? 1 : 0.6,
      child: _mine(
        context,
        Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Text(
                  context.l10n.chatRoleYou,
                  style: TextStyle(
                    fontSize: Fs.small,
                    fontWeight: Fw.medium,
                    color: chatAccent(context),
                  ),
                ),
                const SizedBox(width: 8),
                Text(
                  echo.sent ? context.l10n.chatSent : context.l10n.chatSending,
                  style: TextStyle(fontSize: Fs.small, color: muted),
                ),
              ],
            ),
            const SizedBox(height: 6),
            if (echo.images > 0)
              Text(
                context.l10n.chatImageCount(echo.images),
                style: TextStyle(fontSize: Fs.small, color: muted),
              ),
            if (echo.text.isNotEmpty)
              Text(
                echo.text,
                style: const TextStyle(fontSize: Fs.body, height: Lh.read),
              ),
          ],
        ),
      ),
    );
  }
}

/// 对话最后一行：agent 正在干活。照 Claude Code，转动的星号；有终端底部的状态行
/// （"Thinking… (12s · ↓ 1.2k tokens)"）就照着写，看得出它还在动，否则写正在用的工具。
class WorkingRow extends StatelessWidget {
  const WorkingRow({super.key, this.status, this.tool});

  final String? status;
  final String? tool;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final status = this.status;
    final tool = this.tool;
    final text = status != null && status.isNotEmpty
        ? status
        : tool == null || tool.isEmpty
        ? context.l10n.chatWorking
        : context.l10n.chatUsingTool(tool);
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 4, 16, 12),
      child: Row(
        children: [
          WorkingStar(size: Ic.inline, color: c.accent),
          const SizedBox(width: 8),
          Flexible(
            child: Text(
              text,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(
                fontSize: Fs.small,
                color: c.muted,
                fontFeatures: const [FontFeature.tabularFigures()],
              ),
            ),
          ),
        ],
      ),
    );
  }
}

class MessageBubble extends StatelessWidget {
  const MessageBubble({
    super.key,
    required this.message,
    required this.blocks,
    required this.agent,
  });

  final ChatMessage message;
  final List<ChatBlock> blocks;
  final String? agent;

  @override
  Widget build(BuildContext context) {
    final mine = message.role == 'user';
    final muted = chatMuted(context);
    final who = chatWho(context.l10n, message.role, agent);
    final time = chatTimeLabel(context.l10n, message.timestamp);
    final body = Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            Text(
              who,
              style: TextStyle(
                fontSize: Fs.small,
                fontWeight: Fw.medium,
                color: mine ? chatAccent(context) : muted,
              ),
            ),
            if (time != null) ...[
              const SizedBox(width: 8),
              Text(
                time,
                style: TextStyle(
                  fontSize: Fs.small,
                  color: muted,
                  fontFeatures: const [FontFeature.tabularFigures()],
                ),
              ),
            ],
          ],
        ),
        const SizedBox(height: 6),
        for (final block in blocks) _block(context, message.role, block),
      ],
    );
    // 回复直接铺在底上。只有自己的话靠右、强调色淡底。
    if (!mine) {
      return Padding(
        padding: const EdgeInsets.fromLTRB(16, 4, 16, 10),
        child: body,
      );
    }
    return _mine(context, body);
  }

  Widget _block(BuildContext context, String role, ChatBlock block) {
    final muted = chatMuted(context);
    if (block.type == 'image-ref') {
      return Text(
        block.path ?? block.url ?? context.l10n.chatImage,
        style: TextStyle(fontSize: Fs.small, color: muted),
      );
    }
    final text = block.text ?? '';
    if (role == 'user') {
      return Text(
        text,
        style: const TextStyle(fontSize: Fs.body, height: Lh.read),
      );
    }
    final style = role == 'reasoning'
        ? TextStyle(fontStyle: FontStyle.italic, color: muted, height: Lh.read)
        : role == 'system'
        ? TextStyle(fontSize: Fs.small, color: muted, height: Lh.body)
        : const TextStyle(fontSize: Fs.body, height: Lh.read);
    return MarkdownView(text, style: style);
  }
}

class ToolRun extends StatefulWidget {
  const ToolRun({super.key, required this.blocks, this.initiallyOpen = false});

  final List<ChatBlock> blocks;
  final bool initiallyOpen;

  @override
  State<ToolRun> createState() => _ToolRunState();
}

class _ToolRunState extends State<ToolRun> {
  late var _open = widget.initiallyOpen;

  @override
  Widget build(BuildContext context) {
    final muted = chatMuted(context);
    final count = toolRunCount(widget.blocks);
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          GestureDetector(
            behavior: HitTestBehavior.opaque,
            onTap: () => setState(() => _open = !_open),
            child: Container(
              padding: const EdgeInsets.fromLTRB(8, 4, 8, 4),
              decoration: BoxDecoration(
                color: PosseColors.of(context).fill,
                borderRadius: BorderRadius.circular(Rd.button),
              ),
              child: Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Icon(Icons.build_rounded, size: Ic.inline, color: muted),
                  const SizedBox(width: 6),
                  Text(
                    context.l10n.chatToolCalls(count),
                    style: TextStyle(fontSize: Fs.small, color: muted),
                  ),
                  const SizedBox(width: 2),
                  Icon(
                    _open
                        ? Icons.expand_less_rounded
                        : Icons.expand_more_rounded,
                    size: Ic.small,
                    color: muted,
                  ),
                ],
              ),
            ),
          ),
          if (_open)
            for (final block in widget.blocks) _toolBody(context, block, muted),
        ],
      ),
    );
  }

  Widget _toolBody(BuildContext context, ChatBlock block, Color muted) {
    final title = block.isError
        ? context.l10n.chatToolFailed
        : (block.name?.isNotEmpty ?? false)
        ? block.name!
        : context.l10n.chatRoleTool;
    final detail = block.type == 'tool-result'
        ? (block.output ?? '')
        : _preview(block.input);
    return Container(
      width: double.infinity,
      margin: const EdgeInsets.only(top: 6),
      padding: const EdgeInsets.fromLTRB(10, 8, 10, 8),
      decoration: BoxDecoration(
        color: PosseColors.of(context).code,
        borderRadius: BorderRadius.circular(Rd.button),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            title,
            style: TextStyle(
              fontSize: Fs.small,
              fontWeight: Fw.medium,
              color: block.isError
                  ? Theme.of(context).colorScheme.error
                  : muted,
            ),
          ),
          if (detail.isNotEmpty)
            Text(
              detail,
              maxLines: 8,
              overflow: TextOverflow.ellipsis,
              style: mono(Fs.small, height: Lh.code, color: muted),
            ),
        ],
      ),
    );
  }
}

String _preview(Object? input) {
  if (input == null) return '';
  if (input is String) return input;
  try {
    return const JsonEncoder.withIndent('  ').convert(input);
  } catch (_) {
    return '$input';
  }
}
