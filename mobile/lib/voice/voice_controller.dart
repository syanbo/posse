import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

import 'asr_provider.dart';
import 'voice_config.dart';
import 'voice_service.dart';

/// 语音输入状态机状态。
enum VoiceStage {
  idle,
  starting,
  recording,
  stopping,
  transcribing,
  failedRetryable,
}

/// 录音失败/中断后的可重试上下文。
class VoiceFailedAudio {
  const VoiceFailedAudio({
    required this.path,
    required this.durationMs,
    required this.errorCode,
    required this.config,
    required this.contextTerms,
  });

  final String path;
  final int durationMs;
  final String errorCode;
  final VoiceConfig config;
  final List<String> contextTerms;
}

/// 权限缺失通知（由页面层决定弹确认框跳系统设置或语音设置）。
class VoicePermissionIssue {
  const VoicePermissionIssue({required this.needSpeech});
  final bool needSpeech;
}

/// 跨页面复用的语音输入控制器：
/// - 进程级独占采集：同一时刻只有一个控制器占用麦克风，并靠 [sessionId] 精确路由原生事件；
/// - 全链路操作代次（`_opId`）：授权、启动、停录、读 Key、上传任意一步被取消或 dispose 后立即作废并清理临时文件。
class VoiceController extends ChangeNotifier {
  VoiceController({
    VoicePlatform? platform,
    VoiceSecretStore? secretStore,
    CloudAsrClient? cloudAsr,
    required this.readConfig,
    required this.readContextTerms,
    required this.onResult,
    this.onPermissionDenied,
    this.onNotice,
  }) : _platform = platform ?? voicePlatform,
       _secretStore = secretStore ?? voiceSecretStore,
       _cloudAsr = cloudAsr ?? voiceCloudAsr {
    _eventSub = _platform.events.listen(_onNativeEvent);
  }

  /// 当前占用麦克风的控制器：新控制器启动时自动抢占并让旧控制器安全收尾。
  static VoiceController? _activeOwner;
  static int _sessionSeq = 0;

  final VoicePlatform _platform;
  final VoiceSecretStore _secretStore;
  final CloudAsrClient _cloudAsr;
  final VoiceConfig Function() readConfig;
  final List<String> Function() readContextTerms;
  final void Function(String text) onResult;
  final void Function(VoicePermissionIssue issue)? onPermissionDenied;
  final void Function(String code)? onNotice;

  StreamSubscription<VoiceNativeEvent>? _eventSub;
  Timer? _clock;
  Timer? _nativeFinishGuard;
  VoiceCancelToken? _cancelToken;

  VoiceStage _stage = VoiceStage.idle;
  VoiceStage get stage => _stage;
  bool get isIdle => _stage == VoiceStage.idle;
  bool get isStarting => _stage == VoiceStage.starting;
  bool get isRecording => _stage == VoiceStage.recording;
  bool get isStopping => _stage == VoiceStage.stopping;
  bool get isTranscribing => _stage == VoiceStage.transcribing;
  bool get isBusy =>
      _stage == VoiceStage.starting ||
      _stage == VoiceStage.recording ||
      _stage == VoiceStage.stopping ||
      _stage == VoiceStage.transcribing;
  bool get hasFailedRetry => _stage == VoiceStage.failedRetryable;

  int _elapsedSeconds = 0;
  int get elapsedSeconds => _elapsedSeconds;

  int _maxSeconds = 180;
  int get maxSeconds => _maxSeconds;
  bool get nearLimit => isRecording && _elapsedSeconds >= _maxSeconds - 15;

  /// 最近 20 帧振幅（0..1），供波形条绘制。
  final List<double> _levels = List<double>.filled(20, 0.08, growable: true);
  List<double> get levels => List.unmodifiable(_levels);

  /// 系统原生识别时的实时预览文本。
  String _partialText = '';
  String get partialText => _partialText;

  VoiceFailedAudio? _failed;
  VoiceFailedAudio? get failed => _failed;

  int _opId = 0;
  String? _activeSessionId;
  VoiceConfig? _activeConfig;
  List<String> _activeTerms = const [];
  String? _activeFilePath;
  /// 手动停录返回 null 后短暂保留的同笔会话信息，供紧随其后到达的 interrupted 事件接管落盘音频。
  String? _pendingInterruptedSid;
  VoiceConfig? _pendingInterruptedConfig;
  List<String> _pendingInterruptedTerms = const [];
  /// 已被失败重试或正在转写接管的音频路径，避免旧 start()/finish() 滞后回执或异常分支误删。
  final Set<String> _retainedPaths = <String>{};
  bool _disposed = false;

  bool _isCurrent(int opId) => !_disposed && _opId == opId;

  String _nextSessionId() {
    _sessionSeq++;
    return 'vs_${DateTime.now().microsecondsSinceEpoch}_$_sessionSeq';
  }

  /// 开始一次新的语音录制/识别。
  Future<void> start() async {
    if (isBusy || _disposed) return;

    // 抢占其他页面正在进行的采集，避免跨页共享麦克风。
    final prevOwner = _activeOwner;
    if (prevOwner != null && !identical(prevOwner, this)) {
      unawaited(prevOwner.cancel());
    }
    _activeOwner = this;

    final opId = ++_opId;
    final sid = _nextSessionId();
    // 启动瞬间即锁定配置与上下文词表，避免授权弹窗期间切会话导致上下文漂移。
    final config = readConfig();
    final terms = readContextTerms();
    final needSpeech = config.engine.isNative;

    _stage = VoiceStage.starting;
    _clearPendingInterrupted();
    _activeSessionId = sid;
    _activeConfig = config;
    _activeTerms = terms;
    _partialText = '';
    _elapsedSeconds = 0;
    _resetLevels();
    notifyListeners();

    String? createdPath;
    try {
      if (!needSpeech) {
        final uri = config.engine == VoiceEngine.volcengine
            ? parseVoiceEndpointUri(
                config.baseUrl,
                allowInsecureHttp: config.allowInsecureHttp,
              )
            : resolveOpenAITranscriptionUri(
                config.baseUrl,
                allowInsecureHttp: config.allowInsecureHttp,
              );
        if (uri == null) {
          _abortStart(opId, noticeCode: 'invalid_url');
          return;
        }
        final key = await _secretStore.read(config.engine.secretKey);
        if (!_isCurrent(opId)) return;
        if (key == null || key.trim().isEmpty) {
          _abortStart(opId, noticeCode: 'missing_api_key');
          return;
        }
      } else {
        final caps = await _platform.capabilities(locale: config.nativeLocale);
        if (!_isCurrent(opId)) return;
        if (!caps.nativeAvailable) {
          _abortStart(opId, noticeCode: 'speech_unavailable');
          return;
        }
        _maxSeconds = caps.maxNativeSeconds;
      }

      final perm = await _platform.authorize(needSpeech: needSpeech);
      if (!_isCurrent(opId)) return;
      if (!perm.allGranted) {
        _abortStart(opId);
        onPermissionDenied?.call(VoicePermissionIssue(needSpeech: needSpeech));
        return;
      }

      // 新开录音时清理上一次失败残留音频。
      if (_failed case final prev?) {
        _releaseAndDeletePath(prev.path);
        _failed = null;
      }

      if (needSpeech) {
        final hints = VoicePromptHints.buildTerms(
          vocab: config.vocab,
          contextTerms: terms,
        );
        await _platform.startNativeRecognition(
          sessionId: sid,
          locale: config.nativeLocale,
          hints: hints,
        );
        if (!_isCurrent(opId) || _stage != VoiceStage.starting) {
          if (!_isCurrent(opId)) {
            unawaited(_platform.cancelNativeRecognition(sessionId: sid));
          }
          return;
        }
      } else {
        _maxSeconds = 180;
        createdPath = await _platform.createTempAudioPath(
          config.engine.recordFormat,
        );
        if (!_isCurrent(opId) || _stage != VoiceStage.starting) {
          unawaited(_platform.deleteTempFile(createdPath));
          return;
        }
        _activeFilePath = createdPath;
        await _platform.startRecording(
          sessionId: sid,
          path: createdPath,
          format: config.engine.recordFormat,
        );
        if (!_isCurrent(opId) || _stage != VoiceStage.starting) {
          if (!_isCurrent(opId) && !_retainedPaths.contains(createdPath)) {
            unawaited(_platform.cancelRecording(sessionId: sid));
            unawaited(_platform.deleteTempFile(createdPath));
          }
          return;
        }
      }

      _stage = VoiceStage.recording;
      _startClock();
      notifyListeners();
    } on PlatformException catch (e) {
      if (createdPath != null && !_retainedPaths.contains(createdPath)) {
        unawaited(_platform.deleteTempFile(createdPath));
      }
      debugPrint('voice start failed: ${e.code} ${e.message}');
      _abortStart(
        opId,
        noticeCode: e.code == 'speech_unavailable'
            ? 'speech_unavailable'
            : 'failed',
      );
    } catch (e) {
      if (createdPath != null && !_retainedPaths.contains(createdPath)) {
        unawaited(_platform.deleteTempFile(createdPath));
      }
      debugPrint('voice start failed: $e');
      _abortStart(opId, noticeCode: 'failed');
    }
  }

  void _abortStart(int opId, {String? noticeCode}) {
    if (!_isCurrent(opId)) return;
    _clearActiveSession();
    _stage = _failed != null ? VoiceStage.failedRetryable : VoiceStage.idle;
    notifyListeners();
    if (noticeCode != null) {
      onNotice?.call(noticeCode);
    }
  }

  /// 结束录音并进入转写。
  Future<void> finish() async {
    if (!isRecording || _disposed) return;
    _stopClock();
    final opId = _opId;
    final sid = _activeSessionId;
    final config = _activeConfig;
    final terms = _activeTerms;
    final fallbackPath = _activeFilePath;
    if (config == null || sid == null) {
      _clearActiveSession();
      _stage = VoiceStage.idle;
      notifyListeners();
      return;
    }

    if (config.engine.isNative) {
      _stage = VoiceStage.transcribing;
      _armNativeFinishGuard(opId, sid);
      notifyListeners();
      try {
        final fallback = await _platform.stopNativeRecognition(sessionId: sid);
        if (!_isCurrent(opId)) return;
        if (_stage == VoiceStage.transcribing &&
            fallback != null &&
            fallback.trim().isNotEmpty) {
          _partialText = fallback;
        }
      } catch (_) {
        if (_isCurrent(opId) && _stage == VoiceStage.transcribing) {
          _settleNativeFallbackOrError(opId, 'speech_failed');
        }
      }
      return;
    }

    _stage = VoiceStage.stopping;
    notifyListeners();
    try {
      final rec = await _platform.stopRecording(sessionId: sid);
      final path = rec?.path ?? fallbackPath ?? '';
      // 停录期间若用户取消、中断接管、重录或页面已销毁，仅清理未被接管的音频并终止。
      if (!_isCurrent(opId)) {
        if (path.isNotEmpty && !_retainedPaths.contains(path)) {
          unawaited(_platform.deleteTempFile(path));
        }
        return;
      }
      final durationMs = rec?.durationMs ?? (_elapsedSeconds * 1000);
      _activeFilePath = null;
      if (rec == null || path.isEmpty) {
        // 原生中断可能正异步落盘并将随后推送同 sessionId 的 interrupted 事件：保留短暂接管窗口。
        _pendingInterruptedSid = sid;
        _pendingInterruptedConfig = config;
        _pendingInterruptedTerms = terms;
        _clearActiveSession();
        _stage = VoiceStage.idle;
        onNotice?.call('empty_result');
        notifyListeners();
        return;
      }
      if (rec.interrupted) {
        _clearActiveSession();
        _retainedPaths.add(path);
        _failed = VoiceFailedAudio(
          path: path,
          durationMs: durationMs,
          errorCode: 'interrupted',
          config: config,
          contextTerms: terms,
        );
        _stage = VoiceStage.failedRetryable;
        notifyListeners();
        return;
      }
      _stage = VoiceStage.transcribing;
      notifyListeners();
      await _transcribeCloudFile(
        opId: opId,
        path: path,
        durationMs: durationMs,
        config: config,
        contextTerms: terms,
      );
    } catch (_) {
      if (fallbackPath != null &&
          fallbackPath.isNotEmpty &&
          !_retainedPaths.contains(fallbackPath)) {
        unawaited(_platform.deleteTempFile(fallbackPath));
      }
      if (_isCurrent(opId)) {
        _clearActiveSession();
        _stage = VoiceStage.idle;
        onNotice?.call('failed');
        notifyListeners();
      }
    }
  }

  /// 取消当前启动中、录音中、停录中或正在转写的操作。
  Future<void> cancel() async {
    if (!isBusy) return;
    _opId++;
    _stopClock();
    _cancelNativeFinishGuard();
    _cancelToken?.cancel();
    _cancelToken = null;

    final sid = _activeSessionId;
    final config = _activeConfig;
    final filePath = _activeFilePath;
    _clearActiveSession();
    _partialText = '';
    _stage = _failed != null ? VoiceStage.failedRetryable : VoiceStage.idle;
    if (!_disposed) notifyListeners();

    if (sid != null) {
      if (config?.engine.isNative == true) {
        await _platform.cancelNativeRecognition(sessionId: sid);
      } else {
        await _platform.cancelRecording(sessionId: sid);
      }
    }
    if (filePath != null && filePath.isNotEmpty) {
      await _platform.deleteTempFile(filePath);
    }
  }

  /// 对失败保留的本地音频发起原地重试。
  Future<void> retry() async {
    final item = _failed;
    if (item == null || isBusy || _disposed) return;
    final opId = ++_opId;
    _failed = null;
    _stage = VoiceStage.transcribing;
    notifyListeners();
    await _transcribeCloudFile(
      opId: opId,
      path: item.path,
      durationMs: item.durationMs,
      config: item.config,
      contextTerms: item.contextTerms,
    );
  }

  /// 丢弃失败保留的本地音频。
  Future<void> discardFailed() async {
    final item = _failed;
    _failed = null;
    if (_stage == VoiceStage.failedRetryable) {
      _stage = VoiceStage.idle;
    }
    if (!_disposed) notifyListeners();
    if (item != null) {
      _retainedPaths.remove(item.path);
      await _platform.deleteTempFile(item.path);
    }
  }

  void _releaseAndDeletePath(String path) {
    // 若该路径已被 _failed 接管等待重试，绝不删除。
    if (_failed?.path == path) return;
    _retainedPaths.remove(path);
    unawaited(_platform.deleteTempFile(path));
  }

  Future<void> _transcribeCloudFile({
    required int opId,
    required String path,
    required int durationMs,
    required VoiceConfig config,
    required List<String> contextTerms,
  }) async {
    _retainedPaths.add(path);
    final token = VoiceCancelToken();
    _cancelToken = token;
    try {
      final apiKey = await _secretStore.read(config.engine.secretKey) ?? '';
      if (!_isCurrent(opId) || token.isCancelled) {
        _releaseAndDeletePath(path);
        return;
      }
      final text = await _cloudAsr.transcribe(
        config: config,
        apiKey: apiKey,
        filePath: path,
        contextTerms: contextTerms,
        cancelToken: token,
      );
      if (!_isCurrent(opId) || token.isCancelled) {
        _releaseAndDeletePath(path);
        return;
      }
      _cancelToken = null;
      _clearActiveSession();
      _stage = VoiceStage.idle;
      _releaseAndDeletePath(path);
      notifyListeners();
      onResult(text);
    } on VoiceAsrException catch (e) {
      if (!_isCurrent(opId) || token.isCancelled || e.code == 'cancelled') {
        _releaseAndDeletePath(path);
        return;
      }
      _cancelToken = null;
      _clearActiveSession();
      if (e.code == 'empty_result') {
        _releaseAndDeletePath(path);
        _stage = VoiceStage.idle;
        onNotice?.call('empty_result');
      } else {
        _failed = VoiceFailedAudio(
          path: path,
          durationMs: durationMs,
          errorCode: e.code,
          config: config,
          contextTerms: contextTerms,
        );
        _stage = VoiceStage.failedRetryable;
      }
      notifyListeners();
    } catch (_) {
      if (!_isCurrent(opId) || token.isCancelled) {
        _releaseAndDeletePath(path);
        return;
      }
      _cancelToken = null;
      _clearActiveSession();
      _failed = VoiceFailedAudio(
        path: path,
        durationMs: durationMs,
        errorCode: 'server_error',
        config: config,
        contextTerms: contextTerms,
      );
      _stage = VoiceStage.failedRetryable;
      notifyListeners();
    }
  }

  void _onNativeEvent(VoiceNativeEvent event) {
    if (_disposed) return;
    // 兜底接管：手动 stopRecording 先返回 null 后，同 sessionId 的 interrupted 带有效落盘音频紧随到达。
    if (event is VoiceInterruptedEvent &&
        event.sessionId.isNotEmpty &&
        event.sessionId == _pendingInterruptedSid &&
        isIdle) {
      final cfg = _pendingInterruptedConfig;
      final terms = _pendingInterruptedTerms;
      _clearPendingInterrupted();
      final p = event.path;
      if (p != null && p.isNotEmpty && cfg != null) {
        _retainedPaths.add(p);
        _failed = VoiceFailedAudio(
          path: p,
          durationMs: event.durationMs,
          errorCode: 'interrupted',
          config: cfg,
          contextTerms: terms,
        );
        _stage = VoiceStage.failedRetryable;
        notifyListeners();
      }
      return;
    }

    final sid = _activeSessionId;
    if (sid == null) return;
    // 只消费匹配当前 sessionId 的事件（兼容测试里未填 sessionId 的单控制器事件仅当自身是 activeOwner）。
    if (event.sessionId.isNotEmpty) {
      if (event.sessionId != sid) return;
    } else if (!identical(_activeOwner, this)) {
      return;
    }

    switch (event) {
      case VoiceLevelEvent(:final level):
        if (!isRecording) return;
        _levels.removeAt(0);
        _levels.add(level.clamp(0.06, 1.0));
        notifyListeners();
      case VoicePartialEvent(:final text):
        if (!isStarting && !isRecording && !isTranscribing) return;
        _partialText = text;
        notifyListeners();
      case VoiceFinalEvent(:final text):
        if (!isStarting && !isRecording && !isTranscribing) return;
        _opId++;
        _stopClock();
        _cancelNativeFinishGuard();
        _clearActiveSession();
        _stage = VoiceStage.idle;
        final trimmed = text.trim();
        _partialText = '';
        notifyListeners();
        if (trimmed.isEmpty) {
          onNotice?.call('empty_result');
        } else {
          onResult(trimmed);
        }
      case VoiceInterruptedEvent(:final path, :final text, :final durationMs):
        if (!isStarting && !isRecording && !isStopping && !isTranscribing) {
          return;
        }
        _opId++;
        _stopClock();
        _cancelNativeFinishGuard();
        _cancelToken?.cancel();
        _cancelToken = null;
        final config = _activeConfig;
        final terms = _activeTerms;
        _clearActiveSession();
        if (path != null && path.isNotEmpty && config != null) {
          _retainedPaths.add(path);
          _failed = VoiceFailedAudio(
            path: path,
            durationMs: durationMs,
            errorCode: 'interrupted',
            config: config,
            contextTerms: terms,
          );
          _stage = VoiceStage.failedRetryable;
          notifyListeners();
        } else {
          _stage = VoiceStage.idle;
          final kept = (text ?? _partialText).trim();
          _partialText = '';
          notifyListeners();
          if (kept.isNotEmpty) {
            onResult(kept);
          } else {
            onNotice?.call('interrupted');
          }
        }
      case VoiceErrorEvent(:final code, :final message):
        if (!isStarting && !isRecording && !isStopping && !isTranscribing) {
          return;
        }
        debugPrint('voice native error: $code $message');
        _settleNativeFallbackOrError(_opId, code);
    }
  }

  void _armNativeFinishGuard(int opId, String sid) {
    _cancelNativeFinishGuard();
    _nativeFinishGuard = Timer(const Duration(milliseconds: 3000), () {
      if (!_isCurrent(opId) || _stage != VoiceStage.transcribing) return;
      unawaited(_platform.cancelNativeRecognition(sessionId: sid));
      _settleNativeFallbackOrError(opId, 'speech_failed');
    });
  }

  void _cancelNativeFinishGuard() {
    _nativeFinishGuard?.cancel();
    _nativeFinishGuard = null;
  }

  void _settleNativeFallbackOrError(int opId, String errorCode) {
    if (!_isCurrent(opId)) return;
    _stopClock();
    _cancelNativeFinishGuard();
    final kept = _partialText.trim();
    _clearActiveSession();
    _partialText = '';
    _stage = VoiceStage.idle;
    notifyListeners();
    if (kept.isNotEmpty) {
      onResult(kept);
    } else {
      onNotice?.call(errorCode);
    }
  }

  void _clearPendingInterrupted() {
    _pendingInterruptedSid = null;
    _pendingInterruptedConfig = null;
    _pendingInterruptedTerms = const [];
  }

  void _clearActiveSession() {
    _activeSessionId = null;
    _activeConfig = null;
    _activeTerms = const [];
    _activeFilePath = null;
    if (identical(_activeOwner, this)) {
      _activeOwner = null;
    }
  }

  void _startClock() {
    _stopClock();
    _clock = Timer.periodic(const Duration(seconds: 1), (_) {
      if (_disposed || !isRecording) return;
      _elapsedSeconds += 1;
      if (_elapsedSeconds >= _maxSeconds) {
        onNotice?.call('limit_reached');
        unawaited(finish());
        return;
      }
      notifyListeners();
    });
  }

  void _stopClock() {
    _clock?.cancel();
    _clock = null;
  }

  void _resetLevels() {
    for (var i = 0; i < _levels.length; i++) {
      _levels[i] = 0.08;
    }
  }

  @override
  void dispose() {
    _disposed = true;
    _opId++;
    _stopClock();
    _cancelNativeFinishGuard();
    _cancelToken?.cancel();
    _cancelToken = null;
    _eventSub?.cancel();

    final sid = _activeSessionId;
    final config = _activeConfig;
    final filePath = _activeFilePath;
    _clearActiveSession();

    if (sid != null) {
      if (config?.engine.isNative == true) {
        unawaited(_platform.cancelNativeRecognition(sessionId: sid));
      } else {
        unawaited(_platform.cancelRecording(sessionId: sid));
      }
    }
    if (filePath != null && filePath.isNotEmpty) {
      unawaited(_platform.deleteTempFile(filePath));
    }
    if (_failed case final item?) {
      _failed = null;
      unawaited(_platform.deleteTempFile(item.path));
    }
    super.dispose();
  }
}
