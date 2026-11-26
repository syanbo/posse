import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/foundation.dart' show listEquals;
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../settings/voice_settings_screen.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'voice_config.dart';
import 'voice_controller.dart';
import 'voice_service.dart';

/// 将语音错误码映射为界面语言。
String voiceErrorMessage(AppLocalizations l, String code) => switch (code) {
  'missing_api_key' => l.voiceErrorMissingKey,
  'invalid_url' => l.voiceErrorInvalidUrl,
  'auth_failed' => l.voiceErrorAuth,
  'rate_limited' => l.voiceErrorRateLimit,
  'network' => l.voiceErrorNetwork,
  'timeout' => l.voiceErrorTimeout,
  'empty_result' => l.voiceErrorEmpty,
  'speech_unavailable' => l.voiceErrorUnavailable,
  'limit_reached' => l.voiceLimitReached,
  'interrupted' => l.voiceInterruptedSaved,
  _ => l.voiceErrorFailed,
};

/// 语音引擎的界面显示名。
String voiceEngineLabel(AppLocalizations l, VoiceEngine engine) =>
    switch (engine) {
      VoiceEngine.native => l.voiceEngineNative,
      VoiceEngine.openai => l.voiceEngineOpenAI,
      VoiceEngine.groq => l.voiceEngineGroq,
      VoiceEngine.siliconflow => l.voiceEngineSiliconFlow,
      VoiceEngine.volcengine => l.voiceEngineVolcengine,
      VoiceEngine.custom => l.voiceEngineCustom,
    };

/// 格式化录音时长为 `mm:ss`。
String formatVoiceDuration(int seconds) {
  final s = seconds < 0 ? 0 : seconds;
  final m = s ~/ 60;
  final r = s % 60;
  final mm = m.toString().padLeft(2, '0');
  final ss = r.toString().padLeft(2, '0');
  return '$mm:$ss';
}

/// 权限被拒时弹出确认框引导打开系统设置。
Future<void> showVoicePermissionDialog(
  BuildContext context, {
  required bool needSpeech,
}) async {
  final l = context.l10n;
  final ok = await showPosseConfirm(
    context,
    title: l.voicePermissionTitle,
    body: needSpeech ? l.voiceSpeechPermissionBody : l.voicePermissionBody,
    confirm: l.voiceOpenSettings,
  );
  if (ok) {
    await voicePlatform.openSettings();
  }
}

/// 缺 Key、无离线识别或 URL 无效时弹出快捷引导直达「语音输入」设置页。
Future<void> showVoiceConfigPrompt(
  BuildContext context, {
  required PosseAppModel model,
  required String code,
}) async {
  final l = context.l10n;
  final ok = await showPosseConfirm(
    context,
    title: l.voiceSettingsTitle,
    body: voiceErrorMessage(l, code),
    confirm: l.voiceConfigureCloud,
  );
  if (ok && context.mounted) {
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => VoiceSettingsScreen(model: model),
      ),
    );
  }
}

/// 这些错误码可通过跳转「语音输入」设置解决。
bool isVoiceConfigError(String code) =>
    code == 'missing_api_key' ||
    code == 'speech_unavailable' ||
    code == 'invalid_url';

/// 设置首页「语音输入」一行的摘要：本机引擎只写名字，云端引擎带上 Key 是否已配置；还没读到 Key 状态时只写引擎名。
String voiceSummaryText(AppLocalizations l, VoiceEngine engine, bool? hasKey) {
  if (engine.isNative) return l.voiceSummaryNative;
  final name = voiceEngineLabel(l, engine);
  if (hasKey == null) return name;
  return hasKey
      ? l.voiceSummaryCloudReady(name)
      : l.voiceSummaryCloudMissing(name);
}

/// 输入栏一行的高度：34 的按钮。单行输入框、录音行、转写行都按它对齐，切换状态时栏高不变，终端不跳。
const composerRowHeight = 34.0;

/// 输入栏里的 34×34 图标按钮（收起、选图、加附件、@、麦克风、填入）。
class ComposerIconButton extends StatelessWidget {
  const ComposerIconButton({
    super.key,
    required this.icon,
    required this.tooltip,
    required this.onPressed,
    this.color,
    this.iconSize = Ic.medium,
    this.background,
  });

  final IconData icon;
  final String tooltip;
  final VoidCallback? onPressed;

  /// 不传时可点为 muted、禁用为 faint。
  final Color? color;
  final double iconSize;
  final Color? background;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return IconButton(
      onPressed: onPressed,
      tooltip: tooltip,
      padding: EdgeInsets.zero,
      constraints: const BoxConstraints(),
      style: IconButton.styleFrom(
        fixedSize: const Size.square(composerRowHeight),
        minimumSize: const Size.square(composerRowHeight),
        tapTargetSize: MaterialTapTargetSize.shrinkWrap,
        backgroundColor: background,
      ),
      icon: Icon(
        icon,
        size: iconSize,
        color: color ?? (onPressed == null ? c.faint : c.muted),
      ),
    );
  }
}

/// 输入栏最右边的 34 圆形主按钮：发送、录音的完成。可点时实心强调色；忙时原地换成转圈且不可点，
/// 连点两下第二下落在转圈上，不会碰到别的按钮。
class ComposerSendButton extends StatelessWidget {
  const ComposerSendButton({
    super.key,
    required this.ready,
    required this.tooltip,
    required this.onPressed,
    this.busy = false,
    this.icon = Icons.arrow_upward_rounded,
  });

  final bool ready;
  final bool busy;
  final String tooltip;
  final VoidCallback? onPressed;
  final IconData icon;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final reduced = reduceMotionOf(context);
    final enabled = ready && !busy && onPressed != null;
    return AnimatedContainer(
      duration: reduced ? Duration.zero : const Duration(milliseconds: 150),
      width: composerRowHeight,
      height: composerRowHeight,
      decoration: BoxDecoration(
        color: enabled ? c.accent : c.fill,
        shape: BoxShape.circle,
      ),
      child: Material(
        type: MaterialType.transparency,
        shape: const CircleBorder(),
        child: InkWell(
          customBorder: const CircleBorder(),
          onTap: enabled ? onPressed : null,
          child: Tooltip(
            message: tooltip,
            child: Center(
              child: AnimatedSwitcher(
                duration: reduced
                    ? Duration.zero
                    : const Duration(milliseconds: 150),
                child: busy
                    ? HexSpinner(
                        key: const ValueKey('busy'),
                        size: Ic.small,
                        color: c.muted,
                      )
                    : Icon(
                        icon,
                        key: ValueKey(icon),
                        size: Ic.medium,
                        color: enabled ? Colors.white : c.faint,
                      ),
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// 输入栏内共用的麦克风按钮：支持轻点（免手按开始）与长按（按住说话、松手转写、上滑取消）。
/// 使用 [Listener] 管理轻点与 350ms 长按，避免 Tooltip / InkWell 的内部手势抢占长按。
class VoiceMicButton extends StatefulWidget {
  const VoiceMicButton({
    super.key,
    required this.onPressed,
    this.controller,
    this.active = false,
  });

  final VoidCallback? onPressed;
  final VoiceController? controller;
  final bool active;

  @override
  State<VoiceMicButton> createState() => _VoiceMicButtonState();
}

class _VoiceMicButtonState extends State<VoiceMicButton> {
  static const _holdDelay = Duration(milliseconds: 350);
  static const _moveSlop = 14.0;
  static const _cancelDeltaY = -48.0;

  int? _pointer;
  Offset? _downPos;
  Timer? _holdTimer;
  var _holdTriggered = false;
  var _movedTooFar = false;
  var _armed = false;

  @override
  void dispose() {
    _holdTimer?.cancel();
    super.dispose();
  }

  void _reset() {
    _holdTimer?.cancel();
    _holdTimer = null;
    _pointer = null;
    _downPos = null;
    _holdTriggered = false;
    _movedTooFar = false;
    _armed = false;
  }

  void _onDown(PointerDownEvent e) {
    if (widget.onPressed == null || _pointer != null) return;
    _pointer = e.pointer;
    _downPos = e.position;
    _holdTriggered = false;
    _movedTooFar = false;
    _armed = false;
    final vc = widget.controller;
    if (vc != null && !vc.isBusy) {
      _holdTimer = Timer(_holdDelay, () {
        if (!mounted || _pointer == null || _movedTooFar) return;
        final current = widget.controller;
        if (current == null || current.isBusy) return;
        _holdTriggered = true;
        unawaited(HapticFeedback.mediumImpact());
        current.beginHold();
      });
    }
  }

  void _onMove(PointerMoveEvent e) {
    if (e.pointer != _pointer || _downPos == null) return;
    final delta = e.position - _downPos!;
    if (!_holdTriggered) {
      if (delta.distance > _moveSlop) {
        _movedTooFar = true;
        _holdTimer?.cancel();
        _holdTimer = null;
      }
      return;
    }
    final vc = widget.controller;
    if (vc == null || !vc.isHolding) return;
    final armed = delta.dy < _cancelDeltaY;
    if (armed != _armed) {
      _armed = armed;
      if (armed) unawaited(HapticFeedback.selectionClick());
      vc.setHoldCancelArmed(armed);
    }
  }

  void _onUp(PointerUpEvent e) {
    if (e.pointer != _pointer) return;
    final held = _holdTriggered;
    final cancel = _armed;
    final moved = _movedTooFar;
    _reset();
    if (held) {
      widget.controller?.endHold(cancel: cancel);
    } else if (!moved) {
      widget.onPressed?.call();
    }
  }

  void _onCancel(PointerCancelEvent e) {
    if (e.pointer != _pointer) return;
    final held = _holdTriggered;
    _reset();
    if (held) {
      widget.controller?.endHold(cancel: true);
    }
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final vc = widget.controller;
    final holding = vc != null && vc.isHolding;
    final cancelArmed = vc != null && vc.holdCancelArmed;
    final enabled = widget.onPressed != null;
    final color = holding
        ? (cancelArmed ? c.danger : c.accent)
        : !enabled
        ? c.faint
        : widget.active
        ? c.accent
        : c.muted;

    return Semantics(
      button: true,
      enabled: enabled || holding,
      label: context.l10n.voiceButtonTooltip,
      child: Listener(
        key: const Key('voice-mic-hold-target'),
        behavior: HitTestBehavior.opaque,
        onPointerDown: _onDown,
        onPointerMove: _onMove,
        onPointerUp: _onUp,
        onPointerCancel: _onCancel,
        child: SizedBox(
          key: const Key('voice-mic-button'),
          width: composerRowHeight,
          height: composerRowHeight,
          child: DecoratedBox(
            decoration: BoxDecoration(
              color: widget.active && !holding ? c.tint : null,
              shape: BoxShape.circle,
            ),
            child: Center(
              child: Icon(
                holding && cancelArmed ? Icons.close_rounded : Icons.mic_rounded,
                size: Ic.medium,
                color: color,
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// 输入栏内联语音包装器：
/// - 空输入框且空闲时支持长按按住说话（松手转写、上滑取消），参考微信输入法；有文字时把长按完整交还系统选字/移动光标。
/// - 录音/转写时直接在原输入框位置内联展示已有草稿与实时识别文字，不再在上方额外弹窗或替换整行。
class VoiceInlineField extends StatefulWidget {
  const VoiceInlineField({
    super.key,
    required this.controller,
    required this.voice,
    required this.enabled,
    required this.padding,
    required this.child,
  });

  final TextEditingController controller;
  final VoiceController? voice;
  final bool enabled;
  final EdgeInsetsGeometry padding;
  final Widget child;

  @override
  State<VoiceInlineField> createState() => _VoiceInlineFieldState();
}

class _VoiceInlineFieldState extends State<VoiceInlineField> {
  static const _holdDelay = Duration(milliseconds: 350);
  static const _moveSlop = 14.0;
  static const _cancelDeltaY = -48.0;

  int? _pointer;
  Offset? _downPos;
  Timer? _holdTimer;
  var _holdTriggered = false;
  var _armed = false;

  @override
  void dispose() {
    _holdTimer?.cancel();
    super.dispose();
  }

  void _reset() {
    _holdTimer?.cancel();
    _holdTimer = null;
    _pointer = null;
    _downPos = null;
    _holdTriggered = false;
    _armed = false;
  }

  void _onDown(PointerDownEvent e) {
    final vc = widget.voice;
    if (vc == null || !widget.enabled || vc.isBusy || _pointer != null) return;
    // 仅空输入框响应长按说话；有文字时原样交还 TextField 处理光标与选字。
    if (widget.controller.text.isNotEmpty) return;
    _pointer = e.pointer;
    _downPos = e.position;
    _holdTriggered = false;
    _armed = false;
    _holdTimer = Timer(_holdDelay, () {
      if (!mounted || _pointer == null) return;
      final current = widget.voice;
      if (current == null ||
          !widget.enabled ||
          current.isBusy ||
          widget.controller.text.isNotEmpty) {
        return;
      }
      _holdTriggered = true;
      unawaited(HapticFeedback.mediumImpact());
      current.beginHold();
    });
  }

  void _onMove(PointerMoveEvent e) {
    if (e.pointer != _pointer || _downPos == null) return;
    final delta = e.position - _downPos!;
    if (!_holdTriggered) {
      if (delta.distance > _moveSlop) {
        _reset();
      }
      return;
    }
    final vc = widget.voice;
    if (vc == null || !vc.isHolding) return;
    final armed = delta.dy < _cancelDeltaY;
    if (armed != _armed) {
      _armed = armed;
      if (armed) unawaited(HapticFeedback.selectionClick());
      vc.setHoldCancelArmed(armed);
    }
  }

  void _onUp(PointerUpEvent e) {
    if (e.pointer != _pointer) return;
    final held = _holdTriggered;
    final cancel = _armed;
    _reset();
    if (held) {
      widget.voice?.endHold(cancel: cancel);
    }
  }

  void _onCancel(PointerCancelEvent e) {
    if (e.pointer != _pointer) return;
    final held = _holdTriggered;
    _reset();
    if (held) {
      widget.voice?.endHold(cancel: true);
    }
  }

  @override
  Widget build(BuildContext context) {
    final vc = widget.voice;
    final busy = vc != null && vc.isBusy;

    return Listener(
      behavior: HitTestBehavior.translucent,
      onPointerDown: _onDown,
      onPointerMove: _onMove,
      onPointerUp: _onUp,
      onPointerCancel: _onCancel,
      child: busy
          ? _VoiceInlineText(
              existing: widget.controller.text,
              voice: vc,
              padding: widget.padding,
            )
          : widget.child,
    );
  }
}

/// 录音/转写期间在输入框内部原地显示的实时文本或轻状态提示（单行同高，最多 6 行内滚动）。
class _VoiceInlineText extends StatelessWidget {
  const _VoiceInlineText({
    required this.existing,
    required this.voice,
    required this.padding,
  });

  final String existing;
  final VoiceController voice;
  final EdgeInsetsGeometry padding;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final live = voice.partialText.trim();
    final waiting = voice.isStopping || voice.isTranscribing;

    Widget content;
    if (voice.holdCancelArmed) {
      content = Text(
        l.voiceReleaseToCancel,
        key: const Key('voice-inline-text'),
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
        style: TextStyle(fontSize: Fs.body, height: Lh.body, color: c.danger),
      );
    } else if (live.isNotEmpty) {
      if (existing.isEmpty) {
        content = Text(
          live,
          key: const Key('voice-inline-text'),
          style: TextStyle(fontSize: Fs.body, height: Lh.body, color: c.text),
        );
      } else {
        final sep = existing.endsWith(' ') || existing.endsWith('\n')
            ? ''
            : ' ';
        content = Text.rich(
          TextSpan(
            children: [
              TextSpan(text: '$existing$sep'),
              TextSpan(
                text: live,
                style: TextStyle(color: c.accent),
              ),
            ],
          ),
          key: const Key('voice-inline-text'),
          style: TextStyle(fontSize: Fs.body, height: Lh.body, color: c.text),
        );
      }
    } else if (existing.isNotEmpty) {
      final tail = waiting ? l.voiceTranscribing : l.voiceListening;
      content = Text.rich(
        TextSpan(
          children: [
            TextSpan(text: existing),
            TextSpan(
              text: '  $tail',
              style: TextStyle(color: c.muted),
            ),
          ],
        ),
        key: const Key('voice-inline-text'),
        style: TextStyle(fontSize: Fs.body, height: Lh.body, color: c.text),
      );
    } else {
      final status = waiting
          ? l.voiceTranscribing
          : voice.isHolding
          ? l.voiceSlideUpToCancel
          : l.voiceListening;
      final showTime = voice.isRecording && voice.elapsedSeconds > 0;
      content = Text.rich(
        TextSpan(
          children: [
            TextSpan(text: status),
            if (showTime)
              TextSpan(
                text: ' · ${formatVoiceDuration(voice.elapsedSeconds)}',
                style: TextStyle(
                  color: voice.nearLimit ? c.honey : c.faint,
                  fontFeatures: const [FontFeature.tabularFigures()],
                ),
              ),
          ],
        ),
        key: const Key('voice-inline-text'),
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
        style: TextStyle(fontSize: Fs.body, height: Lh.body, color: c.muted),
      );
    }

    return ConstrainedBox(
      constraints: const BoxConstraints(
        minHeight: composerRowHeight,
        maxHeight: Fs.body * Lh.body * 6 + 12,
      ),
      child: Align(
        alignment: Alignment.centerLeft,
        heightFactor: 1.0,
        child: SingleChildScrollView(
          reverse: true,
          physics: const ClampingScrollPhysics(),
          padding: padding,
          child: content,
        ),
      ),
    );
  }
}

/// 录音/转写时贴在输入框右侧的轻量声波与操作区（不替换整行，不改变输入栏单行高度）。
class VoiceInlineTrailing extends StatefulWidget {
  const VoiceInlineTrailing({super.key, required this.controller});

  final VoiceController controller;

  @override
  State<VoiceInlineTrailing> createState() => _VoiceInlineTrailingState();
}

class _VoiceInlineTrailingState extends State<VoiceInlineTrailing> {
  var _near = false;

  @override
  void initState() {
    super.initState();
    _near = widget.controller.nearLimit;
    widget.controller.addListener(_onVoice);
  }

  @override
  void didUpdateWidget(VoiceInlineTrailing old) {
    super.didUpdateWidget(old);
    if (old.controller == widget.controller) return;
    old.controller.removeListener(_onVoice);
    widget.controller.addListener(_onVoice);
    _near = widget.controller.nearLimit;
  }

  @override
  void dispose() {
    widget.controller.removeListener(_onVoice);
    super.dispose();
  }

  /// 快到单次时长上限时轻震一下，只在跨进那一刻。
  void _onVoice() {
    final near = widget.controller.nearLimit;
    if (near && !_near) unawaited(HapticFeedback.lightImpact());
    _near = near;
  }

  @override
  Widget build(BuildContext context) {
    final controller = widget.controller;
    final c = PosseColors.of(context);
    final l = context.l10n;
    final recording = controller.isRecording;
    final waiting = controller.isStopping || controller.isTranscribing;
    final holding = controller.isHolding;
    final cancelArmed = controller.holdCancelArmed;
    final waveColor = cancelArmed
        ? c.danger
        : controller.nearLimit
        ? c.honey
        : recording
        ? c.accent
        : c.faint;

    return Row(
      key: Key(waiting ? 'voice-transcribing-row' : 'voice-recording-row'),
      mainAxisSize: MainAxisSize.min,
      children: [
        if (!waiting)
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 6),
            child: _VoiceMiniWave(
              key: const Key('voice-mini-wave'),
              levels: controller.levels,
              color: waveColor,
              active: recording,
            ),
          ),
        if (holding)
          const SizedBox.shrink()
        else ...[
          ComposerIconButton(
            key: const Key('voice-cancel-button'),
            icon: Icons.close_rounded,
            tooltip: l.voiceCancelTooltip,
            onPressed: () => unawaited(controller.cancel()),
          ),
          const SizedBox(width: 2),
          ComposerSendButton(
            key: const Key('voice-finish-button'),
            ready: recording,
            busy: waiting,
            icon: Icons.check_rounded,
            tooltip: waiting ? l.voiceTranscribing : l.voiceFinishTooltip,
            onPressed: () => unawaited(controller.finish()),
          ),
        ],
      ],
    );
  }
}

/// 录音/转写时输入胶囊的柔和边框色：默认淡强调色，上滑取消为危险色，临近上限为琥珀色。
Color voiceCapsuleBorderColor(
  PosseColors c, {
  required VoiceController? voice,
  bool focused = false,
}) {
  if (voice != null && voice.isBusy) {
    if (voice.holdCancelArmed) return c.danger.withValues(alpha: 0.7);
    if (voice.nearLimit) return c.honey.withValues(alpha: 0.7);
    return c.accent.withValues(alpha: 0.55);
  }
  return focused ? c.accent.withValues(alpha: 0.6) : c.line;
}

/// 右侧轻量 4 柱小声波：弱对比柔和起伏，仅作为语音识别中的状态反馈，不抢文字视线。
class _VoiceMiniWave extends StatelessWidget {
  const _VoiceMiniWave({
    super.key,
    required this.levels,
    required this.color,
    required this.active,
  });

  final List<double> levels;
  final Color color;
  final bool active;

  @override
  Widget build(BuildContext context) {
    final reduced = reduceMotionOf(context);
    final n = levels.length;
    final samples = <double>[
      for (var i = 0; i < 4; i++)
        reduced || !active
            ? (i == 1 || i == 2 ? 0.42 : 0.24)
            : (n > 0 ? levels[(n - 4 + i).clamp(0, n - 1)] : 0.12),
    ];
    return SizedBox(
      width: 18,
      height: 16,
      child: CustomPaint(
        painter: _MiniWavePainter(
          samples: samples,
          color: color.withValues(alpha: active ? 0.65 : 0.35),
        ),
      ),
    );
  }
}

class _MiniWavePainter extends CustomPainter {
  _MiniWavePainter({required this.samples, required this.color});

  final List<double> samples;
  final Color color;

  static const _bar = 2.5;
  static const _gap = 2.5;

  @override
  void paint(Canvas canvas, Size size) {
    final count = samples.length;
    if (count == 0) return;
    final pitch = _bar + _gap;
    final start = (size.width - (pitch * (count - 1) + _bar)) / 2;
    final cy = size.height / 2;
    final paint = Paint()
      ..color = color
      ..strokeCap = StrokeCap.round
      ..strokeWidth = _bar;
    for (var i = 0; i < count; i++) {
      // 中间两根稍长，形成自然的柔和声波轮廓
      final envelope = (i == 1 || i == 2) ? 1.0 : 0.72;
      final amp = (samples[i].clamp(0.12, 1.0) * envelope).clamp(0.14, 1.0);
      final half = math.max(_bar / 2, (size.height * 0.48) * amp);
      final x = start + pitch * i + _bar / 2;
      canvas.drawLine(
        Offset(x, cy - half + _bar / 2),
        Offset(x, cy + half - _bar / 2),
        paint,
      );
    }
  }

  @override
  bool shouldRepaint(_MiniWavePainter oldDelegate) =>
      oldDelegate.color != color || !listEquals(oldDelegate.samples, samples);
}

/// 失败或中断后保留音频的原地重试条。中断不算出错，用琥珀色（待处理）；真失败用危险色。
class VoiceRetryStrip extends StatelessWidget {
  const VoiceRetryStrip({
    super.key,
    required this.controller,
    this.enabled = true,
  });

  final VoiceController controller;
  final bool enabled;

  @override
  Widget build(BuildContext context) {
    final failed = controller.failed;
    if (failed == null) return const SizedBox.shrink();
    final c = PosseColors.of(context);
    final l = context.l10n;
    final secs = (failed.durationMs / 1000).round();
    final paused = failed.errorCode == 'interrupted';
    final tone = paused ? c.honey : c.danger;

    return Container(
      key: const Key('voice-retry-strip'),
      margin: const EdgeInsets.only(bottom: 6),
      padding: const EdgeInsets.fromLTRB(10, 4, 4, 4),
      decoration: BoxDecoration(
        color: c.soft(tone),
        borderRadius: BorderRadius.circular(Rd.input),
      ),
      child: Row(
        children: [
          Icon(
            paused
                ? Icons.pause_circle_outline_rounded
                : Icons.error_outline_rounded,
            size: Ic.small,
            color: tone,
          ),
          const SizedBox(width: 6),
          if (secs > 0) ...[
            Text(
              formatVoiceDuration(secs),
              style: TextStyle(
                fontSize: Fs.small,
                color: c.muted,
                fontFeatures: const [FontFeature.tabularFigures()],
              ),
            ),
            const SizedBox(width: 8),
          ],
          Expanded(
            child: Text(
              voiceErrorMessage(l, failed.errorCode),
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(fontSize: Fs.small, color: c.text),
            ),
          ),
          TextButton(
            key: const Key('voice-retry-button'),
            onPressed: enabled ? () => unawaited(controller.retry()) : null,
            style: TextButton.styleFrom(
              foregroundColor: c.accent,
              backgroundColor: Colors.transparent,
              padding: const EdgeInsets.symmetric(horizontal: 10),
              minimumSize: const Size(0, 32),
              tapTargetSize: MaterialTapTargetSize.shrinkWrap,
            ),
            child: Text(
              l.voiceRetry,
              style: const TextStyle(fontSize: Fs.small, fontWeight: Fw.medium),
            ),
          ),
          IconButton(
            key: const Key('voice-discard-button'),
            onPressed: enabled
                ? () => unawaited(controller.discardFailed())
                : null,
            tooltip: l.voiceDiscard,
            padding: EdgeInsets.zero,
            constraints: const BoxConstraints(),
            style: IconButton.styleFrom(
              fixedSize: const Size.square(32),
              tapTargetSize: MaterialTapTargetSize.shrinkWrap,
            ),
            icon: Icon(Icons.close_rounded, size: Ic.small, color: c.muted),
          ),
        ],
      ),
    );
  }
}
