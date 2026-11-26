import 'dart:convert';

import 'package:flutter/material.dart';

import '../l10n/l10n.dart';
import '../ui/theme.dart';
import '../voice/voice_bar.dart';
import '../voice/voice_controller.dart';

/// 校验并编码终端「仅填入（不回车）」的字节流：
/// - 手机本地的 DECSET 2004（括号粘贴）状态可能落后于远端 PTY，因此「仅填入」只允许安全单行纯文本，
///   拒绝任何换行（`\n` / `\r`）与所有控制字符（含 Tab、ESC、DEL），防误触发命令或补全；多行草稿请使用「发送」。
List<int>? encodeSafeTerminalPaste(String text) {
  if (text.isEmpty) return null;
  for (final code in text.codeUnits) {
    if (code < 0x20 || code == 0x7f) return null;
  }
  return utf8.encode(text);
}

/// 快捷键栏「草稿 / 语音」键这一下该做什么。
enum DraftKeyAction { open, finish, ignore, collapse }

/// 收着时展开草稿栏并聚焦输入框，让人先打字（录音走栏里的麦克风，引擎没配好也不会一点就弹错打断）；
/// 录音中这个键是亮的，点它是停录并转写，不丢录音；停录、转写中不响应（取消用录音行里的叉）；
/// 其余时候收起（启动中还没录到东西，由调用方一并取消）。
DraftKeyAction draftKeyAction({
  required bool draftOpen,
  required VoiceStage stage,
}) {
  if (!draftOpen) return DraftKeyAction.open;
  return switch (stage) {
    VoiceStage.recording => DraftKeyAction.finish,
    VoiceStage.stopping || VoiceStage.transcribing => DraftKeyAction.ignore,
    _ => DraftKeyAction.collapse,
  };
}

/// 终端页底部的折叠式草稿输入栏（Staged Input）：
/// 浮在 [TerminalKeyBar] 正上方，通过 [onHeightChanged] 把实际高度回报给终端页并入 covered，
/// 不缩小终端本身的高度，避免触发 PTY resize。
class TerminalDraftBar extends StatefulWidget {
  const TerminalDraftBar({
    super.key,
    required this.controller,
    required this.focusNode,
    required this.voice,
    required this.canSend,
    required this.sending,
    required this.onSend,
    required this.onPasteOnly,
    required this.onCollapse,
    required this.onHeightChanged,
  });

  final TextEditingController controller;
  final FocusNode focusNode;
  final VoiceController voice;
  final bool canSend;
  final bool sending;
  final VoidCallback onSend;
  final VoidCallback onPasteOnly;
  final VoidCallback onCollapse;
  final ValueChanged<double> onHeightChanged;

  @override
  State<TerminalDraftBar> createState() => _TerminalDraftBarState();
}

class _TerminalDraftBarState extends State<TerminalDraftBar> {
  final _boxKey = GlobalKey();
  double _lastHeight = 0;

  void _reportHeight() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      final box = _boxKey.currentContext?.findRenderObject();
      if (box is! RenderBox || !box.hasSize) return;
      final h = box.size.height;
      if ((h - _lastHeight).abs() > 0.5) {
        _lastHeight = h;
        widget.onHeightChanged(h);
      }
    });
  }

  @override
  void dispose() {
    if (_lastHeight > 0) {
      final cb = widget.onHeightChanged;
      WidgetsBinding.instance.addPostFrameCallback((_) => cb(0));
    }
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final reduced = reduceMotionOf(context);
    // 展开时淡入并上移 8（和弹框一样）；只改变换和透明度，栏高第一帧就定了，终端不跟着逐帧挪。
    return TweenAnimationBuilder<double>(
      tween: Tween(begin: 0, end: 1),
      duration: reduced ? Duration.zero : const Duration(milliseconds: 180),
      curve: Curves.easeOutCubic,
      builder: (context, t, child) => Opacity(
        opacity: t,
        child: Transform.translate(
          offset: Offset(0, 8 * (1 - t)),
          child: child,
        ),
      ),
      child: ListenableBuilder(
        listenable: Listenable.merge([
          widget.controller,
          widget.voice,
          widget.focusNode,
        ]),
        builder: (context, _) {
          _reportHeight();
          return _buildBar(context);
        },
      ),
    );
  }

  Widget _buildBar(BuildContext context) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final voice = widget.voice;
    final text = widget.controller.text;
    final hasText = text.trim().isNotEmpty;
    final busyVoice = voice.isBusy;
    final canAct = hasText && widget.canSend && !widget.sending && !busyVoice;
    // 与「仅填入」的拦截用同一个判断：多行或含控制字符时先变淡，点了仍弹说明。
    final pasteSafe = encodeSafeTerminalPaste(text) != null;
    final focused = widget.focusNode.hasFocus;

    return Container(
      key: _boxKey,
      decoration: BoxDecoration(
        color: c.band,
        border: Border(top: BorderSide(color: c.line)),
      ),
      child: SafeArea(
        top: false,
        bottom: false,
        child: Padding(
          padding: const EdgeInsets.fromLTRB(10, 6, 10, 6),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              if (voice.hasFailedRetry) VoiceRetryStrip(controller: voice),
              if (showVoicePartial(voice))
                VoicePartialPreview(text: voice.partialText),
              Container(
                key: const Key('terminal-draft-bar'),
                decoration: BoxDecoration(
                  color: c.card,
                  borderRadius: BorderRadius.circular(Rd.composer),
                  border: Border.all(
                    color: focused && !busyVoice
                        ? c.accent.withValues(alpha: 0.6)
                        : c.line,
                  ),
                ),
                padding: const EdgeInsets.all(4),
                child: busyVoice
                    ? VoiceActiveRow(controller: voice)
                    : Row(
                        crossAxisAlignment: CrossAxisAlignment.end,
                        children: [
                          ComposerIconButton(
                            key: const Key('terminal-draft-collapse'),
                            icon: Icons.keyboard_arrow_down_rounded,
                            tooltip: l.terminalDraftHide,
                            onPressed: widget.onCollapse,
                          ),
                          Expanded(
                            child: TextField(
                              key: const Key('terminal-draft-field'),
                              controller: widget.controller,
                              focusNode: widget.focusNode,
                              minLines: 1,
                              maxLines: 6,
                              keyboardType: TextInputType.multiline,
                              textInputAction: TextInputAction.newline,
                              // 草稿是要进终端的命令：iOS 的智能引号、破折号和自动更正会把 " -- 换成排版字符，命令直接坏掉
                              autocorrect: false,
                              smartQuotesType: SmartQuotesType.disabled,
                              smartDashesType: SmartDashesType.disabled,
                              style: TextStyle(
                                fontSize: Fs.body,
                                color: c.text,
                                height: Lh.body,
                              ),
                              decoration: InputDecoration(
                                isDense: true,
                                filled: false,
                                border: InputBorder.none,
                                enabledBorder: InputBorder.none,
                                focusedBorder: InputBorder.none,
                                // 单行 21 + 6 + 6，不超过 34 的按钮：和录音行同高
                                contentPadding: const EdgeInsets.all(6),
                                hintText: l.terminalDraftHint,
                                hintStyle: TextStyle(
                                  fontSize: Fs.body,
                                  color: c.faint,
                                ),
                              ),
                            ),
                          ),
                          // 填入离发送隔一个麦克风：误触的后果是开始录音，不是执行命令
                          ComposerIconButton(
                            key: const Key('terminal-draft-paste'),
                            icon: Icons.input_rounded,
                            tooltip: pasteSafe || !hasText
                                ? l.terminalDraftPaste
                                : l.terminalDraftPasteUnsafe,
                            onPressed: canAct ? widget.onPasteOnly : null,
                            color: canAct && pasteSafe ? c.muted : c.faint,
                          ),
                          VoiceMicButton(
                            onPressed: widget.sending ? null : voice.start,
                          ),
                          const SizedBox(width: 4),
                          ComposerSendButton(
                            key: const Key('terminal-draft-send'),
                            ready: canAct,
                            busy: widget.sending,
                            tooltip: l.terminalDraftSend,
                            onPressed: widget.onSend,
                          ),
                        ],
                      ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
