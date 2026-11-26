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

/// 输入栏内共用的麦克风按钮（与 ChatComposer 按钮同规格）。
class VoiceMicButton extends StatelessWidget {
  const VoiceMicButton({
    super.key,
    required this.onPressed,
    this.active = false,
  });

  final VoidCallback? onPressed;
  final bool active;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return ComposerIconButton(
      key: const Key('voice-mic-button'),
      icon: Icons.mic_rounded,
      tooltip: context.l10n.voiceButtonTooltip,
      onPressed: onPressed,
      background: active ? c.tint : null,
      color: onPressed == null
          ? c.faint
          : active
          ? c.accent
          : c.muted,
    );
  }
}

/// 录音、转写时原地替换输入行（与输入行同高，不跳动布局）。
/// 四种状态位置固定：左边始终是取消，右边的圆在录音时是完成、忙时原地变转圈，
/// 点完成后连点第二下不会落到取消上。启动中（授权、读 Key）先画成还没亮起的录音行，不闪「正在转写」。
class VoiceActiveRow extends StatefulWidget {
  const VoiceActiveRow({super.key, required this.controller});

  final VoiceController controller;

  @override
  State<VoiceActiveRow> createState() => _VoiceActiveRowState();
}

class _VoiceActiveRowState extends State<VoiceActiveRow> {
  var _near = false;

  @override
  void initState() {
    super.initState();
    _near = widget.controller.nearLimit;
    widget.controller.addListener(_onVoice);
  }

  @override
  void didUpdateWidget(VoiceActiveRow old) {
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
    final near = controller.nearLimit;
    final wave = near ? c.honey : c.accent;

    return SizedBox(
      key: Key(waiting ? 'voice-transcribing-row' : 'voice-recording-row'),
      height: composerRowHeight,
      child: Row(
        children: [
          ComposerIconButton(
            key: const Key('voice-cancel-button'),
            icon: Icons.close_rounded,
            tooltip: l.voiceCancelTooltip,
            onPressed: () => unawaited(controller.cancel()),
          ),
          const SizedBox(width: 4),
          if (waiting)
            Expanded(
              child: Text(
                l.voiceTranscribing,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: TextStyle(fontSize: Fs.body, color: c.muted),
              ),
            )
          else ...[
            _RecordingDot(active: recording),
            const SizedBox(width: 6),
            Text(
              formatVoiceDuration(controller.elapsedSeconds),
              style: TextStyle(
                fontSize: Fs.small,
                fontWeight: Fw.medium,
                color: !recording
                    ? c.muted
                    : near
                    ? c.honey
                    : c.text,
                fontFeatures: const [FontFeature.tabularFigures()],
              ),
            ),
            const SizedBox(width: 10),
            Expanded(
              child: _VoiceWaveform(
                levels: controller.levels,
                color: recording ? wave : c.faint,
              ),
            ),
          ],
          const SizedBox(width: 8),
          ComposerSendButton(
            key: const Key('voice-finish-button'),
            ready: recording,
            busy: waiting,
            icon: Icons.check_rounded,
            tooltip: waiting ? l.voiceTranscribing : l.voiceFinishTooltip,
            onPressed: () => unawaited(controller.finish()),
          ),
        ],
      ),
    );
  }
}

/// 录音红点：录音时缓慢呼吸，启动中是静止的淡点；系统要求少用动画时不呼吸。
class _RecordingDot extends StatefulWidget {
  const _RecordingDot({required this.active});

  final bool active;

  @override
  State<_RecordingDot> createState() => _RecordingDotState();
}

class _RecordingDotState extends State<_RecordingDot>
    with SingleTickerProviderStateMixin {
  late final _ctrl = AnimationController(
    vsync: this,
    duration: const Duration(milliseconds: 1200),
    lowerBound: 0.4,
    value: 1,
  );

  void _sync() {
    final run = widget.active && !reduceMotionOf(context);
    if (run && !_ctrl.isAnimating) {
      _ctrl.repeat(reverse: true);
    } else if (!run && _ctrl.isAnimating) {
      _ctrl.stop();
      _ctrl.value = 1;
    }
  }

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    _sync();
  }

  @override
  void didUpdateWidget(_RecordingDot old) {
    super.didUpdateWidget(old);
    _sync();
  }

  @override
  void dispose() {
    _ctrl.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return FadeTransition(
      opacity: _ctrl,
      child: Container(
        width: 8,
        height: 8,
        decoration: BoxDecoration(
          color: widget.active ? c.danger : c.faint,
          shape: BoxShape.circle,
        ),
      ),
    );
  }
}

/// 系统原生识别时的实时预览（浮在输入胶囊正上方）。最多露三行，始终停在最新那几句。
class VoicePartialPreview extends StatelessWidget {
  const VoicePartialPreview({super.key, required this.text});

  final String text;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Container(
      key: const Key('voice-partial-preview'),
      width: double.infinity,
      margin: const EdgeInsets.only(bottom: 6),
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
      decoration: BoxDecoration(
        color: c.raised,
        borderRadius: BorderRadius.circular(Rd.input),
        border: Border.all(color: c.line),
      ),
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxHeight: Fs.body * Lh.body * 3),
        child: SingleChildScrollView(
          reverse: true,
          physics: const ClampingScrollPhysics(),
          child: Text(
            text,
            style: TextStyle(fontSize: Fs.body, color: c.text, height: Lh.body),
          ),
        ),
      ),
    );
  }
}

/// 实时预览在录音和本机识别收尾（转写中）时都显示，免得松手后文字先消失再冒出来。
bool showVoicePartial(VoiceController voice) =>
    (voice.isRecording || voice.isTranscribing) && voice.partialText.isNotEmpty;

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

class _VoiceWaveform extends StatelessWidget {
  const _VoiceWaveform({required this.levels, required this.color});

  final List<double> levels;
  final Color color;

  @override
  Widget build(BuildContext context) {
    return SizedBox(
      height: 20,
      child: CustomPaint(
        painter: _WaveformPainter(levels: levels, color: color),
      ),
    );
  }
}

/// 固定间距的电平柱，整体居中；越早的柱子越淡，看着是从右往左流动。
class _WaveformPainter extends CustomPainter {
  _WaveformPainter({required this.levels, required this.color});

  final List<double> levels;
  final Color color;

  static const _bar = 3.0;
  static const _gap = 3.0;

  @override
  void paint(Canvas canvas, Size size) {
    if (levels.isEmpty || size.width <= 0) return;
    final count = levels.length;
    final full = count * (_bar + _gap) - _gap;
    // 窄到放不下时按宽度压缩间距
    final pitch = full <= size.width ? _bar + _gap : size.width / count;
    final width = math.min(_bar, pitch * 0.6);
    final start = (size.width - (pitch * (count - 1) + width)) / 2;
    final cy = size.height / 2;
    final paint = Paint()
      ..strokeCap = StrokeCap.round
      ..strokeWidth = width;
    for (var i = 0; i < count; i++) {
      final amp = levels[i].clamp(0.08, 1.0);
      final half = math.max(width / 2, (size.height * 0.45) * amp);
      final x = start + pitch * i + width / 2;
      final alpha = count == 1 ? 1.0 : 0.35 + 0.65 * i / (count - 1);
      paint.color = color.withValues(alpha: color.a * alpha);
      canvas.drawLine(
        Offset(x, cy - half + width / 2),
        Offset(x, cy + half - width / 2),
        paint,
      );
    }
  }

  @override
  bool shouldRepaint(_WaveformPainter oldDelegate) =>
      oldDelegate.color != color || !listEquals(oldDelegate.levels, levels);
}
