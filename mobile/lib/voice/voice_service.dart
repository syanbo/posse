import 'dart:async';
import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

/// 手机端语音能力快照。
class VoiceCapabilities {
  const VoiceCapabilities({
    required this.nativeAvailable,
    required this.onDeviceAvailable,
    required this.maxNativeSeconds,
  });

  final bool nativeAvailable;
  final bool onDeviceAvailable;
  final int maxNativeSeconds;
}

/// 权限授权结果。
class VoicePermissionResult {
  const VoicePermissionResult({required this.mic, required this.speech});

  /// `granted` | `denied` | `restricted`
  final String mic;

  /// `granted` | `denied` | `restricted` | `not_needed`
  final String speech;

  bool get micGranted => mic == 'granted';
  bool get speechGranted => speech == 'granted' || speech == 'not_needed';
  bool get allGranted => micGranted && speechGranted;
}

/// 停录结果。
class VoiceRecordingResult {
  const VoiceRecordingResult({
    required this.path,
    required this.durationMs,
    this.interrupted = false,
  });

  final String path;
  final int durationMs;
  final bool interrupted;
}

/// 原生层推送的语音事件（均携带 [sessionId] 供多控制器精确路由）。
sealed class VoiceNativeEvent {
  const VoiceNativeEvent(this.sessionId);
  final String sessionId;
}

class VoiceLevelEvent extends VoiceNativeEvent {
  const VoiceLevelEvent(this.level, {String sessionId = ''}) : super(sessionId);
  final double level;
}

class VoicePartialEvent extends VoiceNativeEvent {
  const VoicePartialEvent(this.text, {String sessionId = ''})
    : super(sessionId);
  final String text;
}

class VoiceFinalEvent extends VoiceNativeEvent {
  const VoiceFinalEvent(this.text, {String sessionId = ''}) : super(sessionId);
  final String text;
}

class VoiceInterruptedEvent extends VoiceNativeEvent {
  const VoiceInterruptedEvent({
    String sessionId = '',
    this.path,
    this.text,
    required this.durationMs,
    required this.reason,
  }) : super(sessionId);
  final String? path;
  final String? text;
  final int durationMs;
  final String reason;
}

class VoiceErrorEvent extends VoiceNativeEvent {
  const VoiceErrorEvent({
    String sessionId = '',
    required this.code,
    required this.message,
  }) : super(sessionId);
  final String code;
  final String message;
}

/// 语音原生桥接接口（便于单元测试 mock）。
abstract class VoicePlatform {
  Stream<VoiceNativeEvent> get events;
  Future<VoiceCapabilities> capabilities({String? locale});
  Future<VoicePermissionResult> authorize({required bool needSpeech});
  Future<bool> openSettings();
  Future<String> createTempAudioPath(String format);
  Future<void> startRecording({
    required String sessionId,
    required String path,
    required String format,
  });
  Future<VoiceRecordingResult?> stopRecording({required String sessionId});
  Future<void> cancelRecording({required String sessionId});
  Future<void> startNativeRecognition({
    required String sessionId,
    String? locale,
    List<String> hints = const [],
  });
  Future<String?> stopNativeRecognition({required String sessionId});
  Future<void> cancelNativeRecognition({required String sessionId});
  Future<void> deleteTempFile(String path);
  Future<void> cleanupStaleAudio();
}

/// API Key 安全存储接口。
abstract class VoiceSecretStore {
  Future<bool> has(String key);
  Future<String?> read(String key);
  Future<bool> save(String key, String value);
  Future<bool> delete(String key);
  Future<void> clearAll();
}

/// 默认原生语音通道实现（posse/voice + posse/voice/events）。
class MethodChannelVoicePlatform implements VoicePlatform {
  MethodChannelVoicePlatform({
    MethodChannel? methodChannel,
    EventChannel? eventChannel,
  }) : _method = methodChannel ?? const MethodChannel('posse/voice'),
       _event = eventChannel ?? const EventChannel('posse/voice/events');

  final MethodChannel _method;
  final EventChannel _event;
  Stream<VoiceNativeEvent>? _events;

  bool get _supported => !kIsWeb && (Platform.isIOS || Platform.isAndroid);

  @override
  Stream<VoiceNativeEvent> get events {
    if (!_supported) return const Stream.empty();
    return _events ??= _event.receiveBroadcastStream().map(_parseEvent);
  }

  static VoiceNativeEvent _parseEvent(dynamic raw) {
    if (raw is! Map) {
      return const VoiceErrorEvent(code: 'bad_event', message: 'invalid event');
    }
    final type = raw['type'] as String? ?? '';
    final sid = raw['sessionId'] as String? ?? '';
    return switch (type) {
      'level' => VoiceLevelEvent(
        ((raw['level'] as num?)?.toDouble() ?? 0).clamp(0.0, 1.0),
        sessionId: sid,
      ),
      'partial' => VoicePartialEvent(
        raw['text'] as String? ?? '',
        sessionId: sid,
      ),
      'final' => VoiceFinalEvent(raw['text'] as String? ?? '', sessionId: sid),
      'interrupted' => VoiceInterruptedEvent(
        sessionId: sid,
        path: raw['path'] as String?,
        text: raw['text'] as String?,
        durationMs: (raw['durationMs'] as num?)?.toInt() ?? 0,
        reason: raw['reason'] as String? ?? 'interrupted',
      ),
      'error' => VoiceErrorEvent(
        sessionId: sid,
        code: raw['code'] as String? ?? 'failed',
        message: raw['message'] as String? ?? 'voice error',
      ),
      _ => VoiceLevelEvent(0, sessionId: sid),
    };
  }

  @override
  Future<VoiceCapabilities> capabilities({String? locale}) async {
    if (!_supported) {
      return const VoiceCapabilities(
        nativeAvailable: false,
        onDeviceAvailable: false,
        maxNativeSeconds: 55,
      );
    }
    try {
      final res = await _method.invokeMapMethod<String, dynamic>(
        'capabilities',
        {if (locale != null && locale.isNotEmpty) 'locale': locale},
      );
      return VoiceCapabilities(
        nativeAvailable: res?['nativeAvailable'] == true,
        onDeviceAvailable: res?['onDeviceAvailable'] == true,
        maxNativeSeconds: (res?['maxNativeSeconds'] as num?)?.toInt() ?? 55,
      );
    } catch (_) {
      return const VoiceCapabilities(
        nativeAvailable: false,
        onDeviceAvailable: false,
        maxNativeSeconds: 55,
      );
    }
  }

  @override
  Future<VoicePermissionResult> authorize({required bool needSpeech}) async {
    if (!_supported) {
      return const VoicePermissionResult(mic: 'denied', speech: 'denied');
    }
    final res = await _method.invokeMapMethod<String, dynamic>('authorize', {
      'needSpeech': needSpeech,
    });
    return VoicePermissionResult(
      mic: res?['mic'] as String? ?? 'denied',
      speech:
          res?['speech'] as String? ?? (needSpeech ? 'denied' : 'not_needed'),
    );
  }

  @override
  Future<bool> openSettings() async {
    if (!_supported) return false;
    try {
      return await _method.invokeMethod<bool>('openSettings') == true;
    } catch (_) {
      return false;
    }
  }

  Future<Directory> _voiceDir() async {
    final dir = Directory('${Directory.systemTemp.path}/posse_voice');
    if (!await dir.exists()) {
      await dir.create(recursive: true);
    }
    return dir;
  }

  @override
  Future<String> createTempAudioPath(String format) async {
    final dir = await _voiceDir();
    final ext = format == 'wav' ? 'wav' : 'm4a';
    final ts = DateTime.now().microsecondsSinceEpoch;
    return '${dir.path}/rec_$ts.$ext';
  }

  @override
  Future<void> startRecording({
    required String sessionId,
    required String path,
    required String format,
  }) async {
    await _method.invokeMethod<void>('startRecording', {
      'sessionId': sessionId,
      'path': path,
      'format': format,
    });
  }

  @override
  Future<VoiceRecordingResult?> stopRecording({
    required String sessionId,
  }) async {
    final res = await _method.invokeMapMethod<String, dynamic>(
      'stopRecording',
      {'sessionId': sessionId},
    );
    final path = res?['path'] as String?;
    if (path == null || path.isEmpty) return null;
    return VoiceRecordingResult(
      path: path,
      durationMs: (res?['durationMs'] as num?)?.toInt() ?? 0,
      interrupted: res?['interrupted'] == true,
    );
  }

  @override
  Future<void> cancelRecording({required String sessionId}) async {
    await _method.invokeMethod<void>('cancelRecording', {
      'sessionId': sessionId,
    });
  }

  @override
  Future<void> startNativeRecognition({
    required String sessionId,
    String? locale,
    List<String> hints = const [],
  }) async {
    await _method.invokeMethod<void>('startNativeRecognition', {
      'sessionId': sessionId,
      if (locale != null && locale.isNotEmpty) 'locale': locale,
      'hints': hints,
    });
  }

  @override
  Future<String?> stopNativeRecognition({required String sessionId}) async {
    return await _method.invokeMethod<String>('stopNativeRecognition', {
      'sessionId': sessionId,
    });
  }

  @override
  Future<void> cancelNativeRecognition({required String sessionId}) async {
    await _method.invokeMethod<void>('cancelNativeRecognition', {
      'sessionId': sessionId,
    });
  }

  @override
  Future<void> deleteTempFile(String path) async {
    if (path.isEmpty) return;
    try {
      final f = File(path);
      if (await f.exists()) await f.delete();
    } catch (_) {}
  }

  @override
  Future<void> cleanupStaleAudio() async {
    if (!_supported) return;
    try {
      final dir = await _voiceDir();
      final cutoff = DateTime.now().subtract(const Duration(hours: 24));
      await for (final entity in dir.list()) {
        if (entity is! File) continue;
        final stat = await entity.stat();
        if (stat.modified.isBefore(cutoff)) {
          await entity.delete();
        }
      }
    } catch (_) {}
  }
}

/// 默认 API Key 安全存储（posse/voice/secret）。
class MethodChannelVoiceSecretStore implements VoiceSecretStore {
  MethodChannelVoiceSecretStore([MethodChannel? channel])
    : _channel = channel ?? const MethodChannel('posse/voice/secret');

  final MethodChannel _channel;

  bool get _supported => !kIsWeb && (Platform.isIOS || Platform.isAndroid);

  @override
  Future<bool> has(String key) async {
    if (!_supported) return false;
    try {
      return await _channel.invokeMethod<bool>('has', {'key': key}) == true;
    } catch (_) {
      return false;
    }
  }

  @override
  Future<String?> read(String key) async {
    if (!_supported) return null;
    try {
      return await _channel.invokeMethod<String>('read', {'key': key});
    } catch (_) {
      return null;
    }
  }

  @override
  Future<bool> save(String key, String value) async {
    if (!_supported || value.isEmpty) return false;
    try {
      return await _channel.invokeMethod<bool>('save', {
            'key': key,
            'value': value,
          }) ==
          true;
    } catch (_) {
      return false;
    }
  }

  @override
  Future<bool> delete(String key) async {
    if (!_supported) return false;
    try {
      final res = await _channel.invokeMethod<Object?>('delete', {'key': key});
      return res != false;
    } catch (_) {
      return false;
    }
  }

  @override
  Future<void> clearAll() async {
    if (!_supported) return;
    try {
      await _channel.invokeMethod<void>('clearAll');
    } catch (_) {}
  }
}

/// 进程级单例（测试可替换为 fake）。
VoicePlatform voicePlatform = MethodChannelVoicePlatform();
VoiceSecretStore voiceSecretStore = MethodChannelVoiceSecretStore();
