import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/l10n/app_localizations.dart';
import 'package:posse_mobile/settings/custom_keys.dart';
import 'package:posse_mobile/settings/settings.dart';
import 'package:posse_mobile/settings/voice_settings_screen.dart';
import 'package:posse_mobile/terminal/terminal.dart';
import 'package:posse_mobile/terminal/terminal_draft_bar.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_mobile/voice/asr_provider.dart';
import 'package:posse_mobile/voice/voice_bar.dart';
import 'package:posse_mobile/voice/voice_config.dart';
import 'package:posse_mobile/voice/voice_controller.dart';
import 'package:posse_mobile/voice/voice_service.dart';

class _FakeVoicePlatform implements VoicePlatform {
  final _events = StreamController<VoiceNativeEvent>.broadcast();
  var caps = const VoiceCapabilities(
    nativeAvailable: true,
    onDeviceAvailable: true,
    maxNativeSeconds: 55,
  );
  var perm = const VoicePermissionResult(mic: 'granted', speech: 'granted');
  var recording = false;
  var nativeRunning = false;
  String? lastSessionId;
  String? lastRecordedPath;
  String? lastFormat;
  final deletedPaths = <String>[];
  final cancelledSessions = <String>[];
  List<String> lastHints = const [];
  Completer<VoiceRecordingResult?>? delayedStop;
  Completer<void>? delayedStart;

  @override
  Stream<VoiceNativeEvent> get events => _events.stream;

  void emit(VoiceNativeEvent e) => _events.add(e);

  @override
  Future<VoiceCapabilities> capabilities({String? locale}) async => caps;

  @override
  Future<VoicePermissionResult> authorize({required bool needSpeech}) async =>
      perm;

  @override
  Future<bool> openSettings() async => true;

  @override
  Future<String> createTempAudioPath(String format) async =>
      '/tmp/posse_voice_test_${DateTime.now().microsecondsSinceEpoch}.$format';

  @override
  Future<void> startRecording({
    required String sessionId,
    required String path,
    required String format,
  }) async {
    recording = true;
    lastSessionId = sessionId;
    lastRecordedPath = path;
    lastFormat = format;
    if (delayedStart case final gate?) {
      await gate.future;
    }
  }

  @override
  Future<VoiceRecordingResult?> stopRecording({
    required String sessionId,
  }) async {
    if (delayedStop case final gate?) {
      final res = await gate.future;
      recording = false;
      return res;
    }
    if (!recording) return null;
    recording = false;
    return VoiceRecordingResult(
      path: lastRecordedPath ?? '/tmp/posse_voice_test.m4a',
      durationMs: 4200,
    );
  }

  @override
  Future<void> cancelRecording({required String sessionId}) async {
    cancelledSessions.add(sessionId);
    recording = false;
  }

  @override
  Future<void> startNativeRecognition({
    required String sessionId,
    String? locale,
    List<String> hints = const [],
  }) async {
    nativeRunning = true;
    lastSessionId = sessionId;
    lastHints = hints;
  }

  @override
  Future<String?> stopNativeRecognition({required String sessionId}) async {
    nativeRunning = false;
    return null;
  }

  @override
  Future<void> cancelNativeRecognition({required String sessionId}) async {
    cancelledSessions.add(sessionId);
    nativeRunning = false;
  }

  @override
  Future<void> deleteTempFile(String path) async {
    deletedPaths.add(path);
  }

  @override
  Future<void> cleanupStaleAudio() async {}

  void close() => _events.close();
}

class _MemorySecretStore implements VoiceSecretStore {
  final map = <String, String>{};
  var failSave = false;
  Completer<bool>? delayedSave;

  @override
  Future<bool> has(String key) async => map.containsKey(key);

  @override
  Future<String?> read(String key) async => map[key];

  @override
  Future<bool> save(String key, String value) async {
    if (delayedSave case final gate?) {
      final ok = await gate.future;
      if (ok && value.isNotEmpty) map[key] = value;
      return ok;
    }
    if (failSave || value.isEmpty) return false;
    map[key] = value;
    return true;
  }

  @override
  Future<bool> delete(String key) async {
    map.remove(key);
    return true;
  }

  @override
  Future<void> clearAll() async {
    map.clear();
  }
}

class _RealHttpOverrides extends HttpOverrides {}

class _FakeCloudAsr implements CloudAsrClient {
  Future<String> Function(
    VoiceConfig config,
    String apiKey,
    String filePath,
    VoiceCancelToken? cancelToken,
  )?
  handler;
  var calls = 0;

  @override
  Future<String> transcribe({
    required VoiceConfig config,
    required String apiKey,
    required String filePath,
    required List<String> contextTerms,
    VoiceCancelToken? cancelToken,
  }) async {
    calls++;
    return handler!(config, apiKey, filePath, cancelToken);
  }
}

Widget _wrap(Widget child) => MaterialApp(
  locale: const Locale('zh'),
  localizationsDelegates: AppLocalizations.localizationsDelegates,
  supportedLocales: AppLocalizations.supportedLocales,
  theme: ThemeData.dark().copyWith(extensions: const [PosseColors.darkColors]),
  home: Scaffold(body: child),
);

void main() {
  group('VoiceConfig & PromptHints & DraftMerge & PasteSafety', () {
    test('buildTerms去重并受100项上限约束，buildOpenAIPrompt限制长度', () {
      final terms = VoicePromptHints.buildTerms(
        contextTerms: ['posse', 'main', 'Claude'],
        vocab: 'Posse, worktree, rebase，Tailscale\nQUIC',
      );
      expect(terms, [
        'posse',
        'main',
        'Claude',
        'worktree',
        'rebase',
        'Tailscale',
        'QUIC',
      ]);

      final prompt = VoicePromptHints.buildOpenAIPrompt(
        contextTerms: ['posse'],
        vocab: List.generate(80, (i) => 'LongTermIdentifier$i').join(', '),
        maxChars: 120,
      );
      expect(prompt.length, lessThanOrEqualTo(120));
      expect(prompt, contains('posse'));
    });

    test('mergeVoiceDraft、insertRawDraft 与 insertPathDraft 各自职责清晰', () {
      final ascii = mergeVoiceDraft(
        current: 'fix',
        start: 3,
        end: 3,
        incoming: 'bug',
      );
      expect(ascii.text, 'fix bug');
      expect(ascii.cursor, 7);

      final cn = mergeVoiceDraft(
        current: '帮我看下',
        start: 4,
        end: 4,
        incoming: '这个报错',
      );
      expect(cn.text, '帮我看下这个报错');
      expect(cn.cursor, 8);

      // 剪贴板粘贴保留行首缩进和换行，不做 trim
      final raw = insertRawDraft(
        current: 'code:\n',
        start: 6,
        end: 6,
        raw: '  indented\n',
      );
      expect(raw.text, 'code:\n  indented\n');

      // 连续两个绝对路径自动补空格分隔
      final p1 = insertPathDraft(
        current: '/tmp/a.png',
        start: 10,
        end: 10,
        path: '/tmp/b.png',
      );
      expect(p1.text, '/tmp/a.png /tmp/b.png');
    });

    test('encodeSafeTerminalPaste 拦截所有多行、Tab 与控制序列，仅放行安全单行', () {
      expect(encodeSafeTerminalPaste('echo first\necho second'), isNull);
      expect(encodeSafeTerminalPaste('echo first\recho second'), isNull);
      expect(encodeSafeTerminalPaste('cmd\targ'), isNull);
      expect(encodeSafeTerminalPaste('safe\x1b[201~evil'), isNull);
      expect(encodeSafeTerminalPaste('bad\x03ctrl'), isNull);
      final single = encodeSafeTerminalPaste('ls -la');
      expect(utf8.decode(single!), 'ls -la');
    });

    test('resolveOpenAITranscriptionUri 默认强制 HTTPS，局域网明文仅限私网/回环 IP 字面量', () {
      expect(
        resolveOpenAITranscriptionUri('https://api.openai.com/v1')?.toString(),
        'https://api.openai.com/v1/audio/transcriptions',
      );
      expect(
        resolveOpenAITranscriptionUri('http://192.168.1.2:8000/v1'),
        isNull,
      );
      expect(
        resolveOpenAITranscriptionUri(
          'http://192.168.1.2:8000/v1',
          allowInsecureHttp: true,
        )?.toString(),
        'http://192.168.1.2:8000/v1/audio/transcriptions',
      );
      // 即使开启局域网明文 HTTP，也拒绝域名/.local 与公网 IP
      expect(
        resolveOpenAITranscriptionUri(
          'http://server.local/v1',
          allowInsecureHttp: true,
        ),
        isNull,
      );
      expect(
        resolveOpenAITranscriptionUri(
          'http://evil.example.com/v1',
          allowInsecureHttp: true,
        ),
        isNull,
      );
      expect(
        resolveOpenAITranscriptionUri(
          'http://8.8.8.8/v1',
          allowInsecureHttp: true,
        ),
        isNull,
      );
      expect(resolveOpenAITranscriptionUri('ftp://bad.host'), isNull);
    });

    test('AppSettings序列化与反序列化保留语音与明文开关配置，不存Key', () {
      final s = AppSettings(
        voiceEngine: 'volcengine',
        voiceBaseUrl: 'https://example.com/flash',
        voiceModel: 'volc.custom',
        voiceLanguage: 'zh',
        voiceVocab: 'Posse, flterm',
        voiceAllowInsecureHttp: true,
        terminalDraftOpenByDefault: true,
      );
      final encoded = jsonEncode(s.toJson());
      final decoded = AppSettings.decode(encoded);
      expect(decoded.voiceEngine, 'volcengine');
      expect(decoded.voiceBaseUrl, 'https://example.com/flash');
      expect(decoded.voiceModel, 'volc.custom');
      expect(decoded.voiceLanguage, 'zh');
      expect(decoded.voiceVocab, 'Posse, flterm');
      expect(decoded.voiceAllowInsecureHttp, isTrue);
      expect(decoded.terminalDraftOpenByDefault, isTrue);
    });

    test('normalizeBarKeys把新增voice内置键插在paste后面', () {
      final old = [
        const BarKey.builtin('image'),
        const BarKey.builtin('paste'),
        const BarKey.builtin('Esc'),
      ];
      final normalized = normalizeBarKeys(old);
      final ids = [for (final k in normalized) k.builtin];
      expect(ids.indexOf('voice'), ids.indexOf('paste') + 1);
    });
  });

  group('HttpCloudAsrClient', () {
    test('OpenAI兼容multipart请求与响应解析', () async {
      await HttpOverrides.runWithHttpOverrides(() async {
        final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
        addTearDown(() => server.close(force: true));
        String? gotAuth;
        String? gotBody;
        server.listen((req) async {
          gotAuth = req.headers.value(HttpHeaders.authorizationHeader);
          gotBody = await utf8.decodeStream(req);
          req.response.statusCode = 200;
          req.response.write(jsonEncode({'text': '修复 worktree 冲突'}));
          await req.response.close();
        });

        final tmp = File(
          '${Directory.systemTemp.path}/posse_asr_test_${DateTime.now().microsecondsSinceEpoch}.m4a',
        );
        await tmp.writeAsBytes([1, 2, 3, 4]);
        addTearDown(() async {
          if (await tmp.exists()) await tmp.delete();
        });

        final client = HttpCloudAsrClient(allowInsecureHttpForTest: true);
        final text = await client.transcribe(
          config: VoiceConfig(
            engine: VoiceEngine.openai,
            baseUrl: 'http://127.0.0.1:${server.port}/v1',
            model: 'gpt-4o-mini-transcribe',
            language: 'zh',
            vocab: 'Posse',
          ),
          apiKey: 'sk-test-123',
          filePath: tmp.path,
          contextTerms: ['worktree'],
        );
        expect(text, '修复 worktree 冲突');
        expect(gotAuth, 'Bearer sk-test-123');
        expect(gotBody, contains('gpt-4o-mini-transcribe'));
        expect(gotBody, contains('worktree'));
      }, _RealHttpOverrides());
    });

    test('火山豆包Flash HTTP成功与业务状态码校验', () async {
      await HttpOverrides.runWithHttpOverrides(() async {
        final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
        addTearDown(() => server.close(force: true));
        var mode = 'ok';
        server.listen((req) async {
          await utf8.decodeStream(req);
          req.response.statusCode = 200;
          if (mode == 'ok') {
            req.response.headers.set('X-Api-Status-Code', '20000000');
            req.response.write(
              jsonEncode({
                'result': {'text': '帮我跑一下单测'},
              }),
            );
          } else if (mode == 'silent') {
            req.response.headers.set('X-Api-Status-Code', '20000003');
            req.response.write(
              jsonEncode({
                'result': {'text': ''},
              }),
            );
          } else if (mode == 'missing_header') {
            req.response.write('<html>gateway error</html>');
          } else if (mode == 'param_err') {
            req.response.headers.set('X-Api-Status-Code', '45000001');
            req.response.write(jsonEncode({}));
          } else if (mode == 'redirect') {
            req.response.statusCode = 303;
            req.response.headers.set(
              HttpHeaders.locationHeader,
              'http://127.0.0.1:9/leak',
            );
          } else {
            req.response.headers.set('X-Api-Status-Code', '55000001');
            req.response.headers.set('X-Tt-Logid', 'log-err-1');
            req.response.write(jsonEncode({}));
          }
          await req.response.close();
        });

        final tmp = File(
          '${Directory.systemTemp.path}/posse_volc_test_${DateTime.now().microsecondsSinceEpoch}.wav',
        );
        await tmp.writeAsBytes([5, 6, 7, 8]);
        addTearDown(() async {
          if (await tmp.exists()) await tmp.delete();
        });

        final client = HttpCloudAsrClient(allowInsecureHttpForTest: true);
        final cfg = VoiceConfig(
          engine: VoiceEngine.volcengine,
          baseUrl: 'http://127.0.0.1:${server.port}/flash',
          model: 'volc.bigasr.auc_turbo',
          language: '',
          vocab: '',
        );

        expect(
          await client.transcribe(
            config: cfg,
            apiKey: 'volc-key-xyz',
            filePath: tmp.path,
            contextTerms: const [],
          ),
          '帮我跑一下单测',
        );

        mode = 'silent';
        await expectLater(
          client.transcribe(
            config: cfg,
            apiKey: 'volc-key-xyz',
            filePath: tmp.path,
            contextTerms: const [],
          ),
          throwsA(
            isA<VoiceAsrException>().having(
              (e) => e.code,
              'code',
              'empty_result',
            ),
          ),
        );

        mode = 'missing_header';
        await expectLater(
          client.transcribe(
            config: cfg,
            apiKey: 'volc-key-xyz',
            filePath: tmp.path,
            contextTerms: const [],
          ),
          throwsA(
            isA<VoiceAsrException>().having(
              (e) => e.code,
              'code',
              'server_error',
            ),
          ),
        );

        mode = 'param_err';
        await expectLater(
          client.transcribe(
            config: cfg,
            apiKey: 'volc-key-xyz',
            filePath: tmp.path,
            contextTerms: const [],
          ),
          throwsA(
            isA<VoiceAsrException>().having(
              (e) => e.code,
              'code',
              'server_error',
            ),
          ),
        );

        mode = 'redirect';
        await expectLater(
          client.transcribe(
            config: cfg,
            apiKey: 'volc-key-xyz',
            filePath: tmp.path,
            contextTerms: const [],
          ),
          throwsA(
            isA<VoiceAsrException>().having(
              (e) => e.code,
              'code',
              'server_error',
            ),
          ),
        );

        mode = 'biz_err';
        await expectLater(
          client.transcribe(
            config: cfg,
            apiKey: 'volc-key-xyz',
            filePath: tmp.path,
            contextTerms: const [],
          ),
          throwsA(
            isA<VoiceAsrException>().having(
              (e) => e.code,
              'code',
              'server_error',
            ),
          ),
        );
      }, _RealHttpOverrides());
    });
  });

  group('VoiceController 状态机、竞态与隔离', () {
    test('云端转写失败保留音频，重试成功后删除临时文件', () async {
      final platform = _FakeVoicePlatform();
      final secrets = _MemorySecretStore()..map['asr_openai'] = 'sk-ok';
      final asr = _FakeCloudAsr();
      var shouldFail = true;
      asr.handler = (_, _, _, _) async {
        if (shouldFail) throw const VoiceAsrException('timeout');
        return '重试后的草稿';
      };
      String? received;
      final ctrl = VoiceController(
        platform: platform,
        secretStore: secrets,
        cloudAsr: asr,
        readConfig: () => const VoiceConfig(
          engine: VoiceEngine.openai,
          baseUrl: 'https://api.openai.com/v1',
          model: 'gpt-4o-mini-transcribe',
          language: '',
          vocab: '',
        ),
        readContextTerms: () => ['posse'],
        onResult: (t) => received = t,
      );
      addTearDown(() {
        ctrl.dispose();
        platform.close();
      });

      await ctrl.start();
      expect(ctrl.isRecording, isTrue);
      expect(platform.lastFormat, 'm4a');
      final recordedPath = platform.lastRecordedPath!;

      await ctrl.finish();
      expect(ctrl.hasFailedRetry, isTrue);
      expect(ctrl.failed?.errorCode, 'timeout');
      expect(platform.deletedPaths, isEmpty);

      shouldFail = false;
      await ctrl.retry();
      expect(ctrl.isIdle, isTrue);
      expect(received, '重试后的草稿');
      expect(platform.deletedPaths, contains(recordedPath));
    });

    test('停录返回前取消：不启动云端上传并清理迟到文件', () async {
      final platform = _FakeVoicePlatform();
      final secrets = _MemorySecretStore()..map['asr_openai'] = 'sk-ok';
      final asr = _FakeCloudAsr()..handler = (_, _, _, _) async => '不应出现的结果';
      String? received;
      final ctrl = VoiceController(
        platform: platform,
        secretStore: secrets,
        cloudAsr: asr,
        readConfig: () => const VoiceConfig(
          engine: VoiceEngine.openai,
          baseUrl: 'https://api.openai.com/v1',
          model: 'gpt-4o-mini-transcribe',
          language: '',
          vocab: '',
        ),
        readContextTerms: () => const [],
        onResult: (t) => received = t,
      );
      addTearDown(() {
        ctrl.dispose();
        platform.close();
      });

      await ctrl.start();
      final gate = Completer<VoiceRecordingResult?>();
      platform.delayedStop = gate;

      final finishFuture = ctrl.finish();
      expect(ctrl.isStopping, isTrue);
      await ctrl.cancel();
      gate.complete(
        const VoiceRecordingResult(path: '/tmp/late.m4a', durationMs: 2000),
      );
      await finishFuture;

      expect(asr.calls, 0);
      expect(received, isNull);
      expect(platform.deletedPaths, contains('/tmp/late.m4a'));
    });

    test(
      '启动回执到达前收到 interrupted：进入 failedRetryable 且不被回执覆盖为 recording',
      () async {
        final platform = _FakeVoicePlatform();
        final secrets = _MemorySecretStore()..map['asr_openai'] = 'sk-ok';
        final ctrl = VoiceController(
          platform: platform,
          secretStore: secrets,
          cloudAsr: _FakeCloudAsr(),
          readConfig: () => const VoiceConfig(
            engine: VoiceEngine.openai,
            baseUrl: 'https://api.openai.com/v1',
            model: 'gpt-4o-mini-transcribe',
            language: '',
            vocab: '',
          ),
          readContextTerms: () => const [],
          onResult: (_) {},
        );
        addTearDown(() {
          ctrl.dispose();
          platform.close();
        });

        final gate = Completer<void>();
        platform.delayedStart = gate;
        final startFuture = ctrl.start();
        await Future<void>.delayed(Duration.zero);
        await Future<void>.delayed(Duration.zero);

        final sid = platform.lastSessionId!;
        final path = platform.lastRecordedPath!;
        platform.emit(
          VoiceInterruptedEvent(
            sessionId: sid,
            path: path,
            durationMs: 1500,
            reason: 'background',
          ),
        );
        await Future<void>.delayed(Duration.zero);
        expect(ctrl.hasFailedRetry, isTrue);

        gate.complete();
        await startFuture;
        expect(ctrl.hasFailedRetry, isTrue);
        expect(ctrl.isRecording, isFalse);
        expect(platform.deletedPaths, isNot(contains(path)));
      },
    );

    test('启动期中断后立即重试，旧 start 回执到达不误删正在重试的文件', () async {
      final platform = _FakeVoicePlatform();
      final secrets = _MemorySecretStore()..map['asr_openai'] = 'sk-ok';
      final retryGate = Completer<String>();
      final asr = _FakeCloudAsr()..handler = (_, _, _, _) => retryGate.future;
      String? received;
      final ctrl = VoiceController(
        platform: platform,
        secretStore: secrets,
        cloudAsr: asr,
        readConfig: () => const VoiceConfig(
          engine: VoiceEngine.openai,
          baseUrl: 'https://api.openai.com/v1',
          model: 'gpt-4o-mini-transcribe',
          language: '',
          vocab: '',
        ),
        readContextTerms: () => const [],
        onResult: (t) => received = t,
      );
      addTearDown(() {
        ctrl.dispose();
        platform.close();
      });

      final startGate = Completer<void>();
      platform.delayedStart = startGate;
      final startFuture = ctrl.start();
      await Future<void>.delayed(Duration.zero);
      await Future<void>.delayed(Duration.zero);

      final sid = platform.lastSessionId!;
      final path = platform.lastRecordedPath!;
      platform.emit(
        VoiceInterruptedEvent(
          sessionId: sid,
          path: path,
          durationMs: 1800,
          reason: 'background',
        ),
      );
      await Future<void>.delayed(Duration.zero);
      expect(ctrl.hasFailedRetry, isTrue);

      // 用户立即重试，此时旧 start 回执才到达：不得误删正在上传的 path。
      final retryFuture = ctrl.retry();
      await Future<void>.delayed(Duration.zero);
      startGate.complete();
      await startFuture;
      expect(platform.deletedPaths, isNot(contains(path)));

      retryGate.complete('重试成功文本');
      await retryFuture;
      expect(received, '重试成功文本');
      expect(platform.deletedPaths, contains(path));
    });

    test('手动停录与中断交错：两种到达顺序均保留重试音频且不误删', () async {
      // 顺序 1：interrupted 先到，迟到的 stopRecording(null) 不得删除已接管音频
      final p1 = _FakeVoicePlatform();
      final s1 = _MemorySecretStore()..map['asr_openai'] = 'sk-ok';
      final c1 = VoiceController(
        platform: p1,
        secretStore: s1,
        cloudAsr: _FakeCloudAsr(),
        readConfig: () => const VoiceConfig(
          engine: VoiceEngine.openai,
          baseUrl: 'https://api.openai.com/v1',
          model: 'gpt-4o-mini-transcribe',
          language: '',
          vocab: '',
        ),
        readContextTerms: () => const [],
        onResult: (_) {},
      );
      addTearDown(() {
        c1.dispose();
        p1.close();
      });

      await c1.start();
      final sid1 = p1.lastSessionId!;
      final path1 = p1.lastRecordedPath!;
      final stopGate1 = Completer<VoiceRecordingResult?>();
      p1.delayedStop = stopGate1;
      final finishFuture1 = c1.finish();
      p1.emit(
        VoiceInterruptedEvent(
          sessionId: sid1,
          path: path1,
          durationMs: 2200,
          reason: 'background',
        ),
      );
      await Future<void>.delayed(Duration.zero);
      expect(c1.hasFailedRetry, isTrue);

      stopGate1.complete(null);
      await finishFuture1;
      expect(c1.hasFailedRetry, isTrue);
      expect(p1.deletedPaths, isNot(contains(path1)));

      // 顺序 2：stopRecording 先返回 null，紧随其后的同 sessionId interrupted 仍接管音频
      final p2 = _FakeVoicePlatform();
      final s2 = _MemorySecretStore()..map['asr_openai'] = 'sk-ok';
      final c2 = VoiceController(
        platform: p2,
        secretStore: s2,
        cloudAsr: _FakeCloudAsr(),
        readConfig: () => const VoiceConfig(
          engine: VoiceEngine.openai,
          baseUrl: 'https://api.openai.com/v1',
          model: 'gpt-4o-mini-transcribe',
          language: '',
          vocab: '',
        ),
        readContextTerms: () => const [],
        onResult: (_) {},
      );
      addTearDown(() {
        c2.dispose();
        p2.close();
      });

      await c2.start();
      final sid2 = p2.lastSessionId!;
      final path2 = p2.lastRecordedPath!;
      p2.delayedStop = Completer<VoiceRecordingResult?>()..complete(null);
      await c2.finish();
      expect(c2.isIdle, isTrue);

      p2.emit(
        VoiceInterruptedEvent(
          sessionId: sid2,
          path: path2,
          durationMs: 2500,
          reason: 'audio_interrupted',
        ),
      );
      await Future<void>.delayed(Duration.zero);
      expect(c2.hasFailedRetry, isTrue);
      expect(c2.failed?.path, path2);
      expect(p2.deletedPaths, isNot(contains(path2)));

      // 顺序 3：stopRecording 携带 interrupted: true，直接进 failedRetryable，云端转写 0 次
      final p3 = _FakeVoicePlatform();
      final s3 = _MemorySecretStore()..map['asr_openai'] = 'sk-ok';
      final asr3 = _FakeCloudAsr()..handler = (_, _, _, _) async => '不应调用';
      final c3 = VoiceController(
        platform: p3,
        secretStore: s3,
        cloudAsr: asr3,
        readConfig: () => const VoiceConfig(
          engine: VoiceEngine.openai,
          baseUrl: 'https://api.openai.com/v1',
          model: 'gpt-4o-mini-transcribe',
          language: '',
          vocab: '',
        ),
        readContextTerms: () => const [],
        onResult: (_) {},
      );
      addTearDown(() {
        c3.dispose();
        p3.close();
      });

      await c3.start();
      final path3 = p3.lastRecordedPath!;
      p3.delayedStop = Completer<VoiceRecordingResult?>()
        ..complete(
          VoiceRecordingResult(
            path: path3,
            durationMs: 3000,
            interrupted: true,
          ),
        );
      await c3.finish();
      expect(asr3.calls, 0);
      expect(c3.hasFailedRetry, isTrue);
      expect(c3.failed?.path, path3);
      expect(p3.deletedPaths, isNot(contains(path3)));

      // 顺序 4：极端场景下有效 stop 启动转写后同路径 interrupted 到达，取消旧令牌且不删重试文件
      final p4 = _FakeVoicePlatform();
      final s4 = _MemorySecretStore()..map['asr_openai'] = 'sk-ok';
      VoiceCancelToken? capturedToken;
      final asrGate = Completer<String>();
      final asr4 = _FakeCloudAsr()
        ..handler = (_, _, _, token) {
          capturedToken = token;
          return asrGate.future;
        };
      final c4 = VoiceController(
        platform: p4,
        secretStore: s4,
        cloudAsr: asr4,
        readConfig: () => const VoiceConfig(
          engine: VoiceEngine.openai,
          baseUrl: 'https://api.openai.com/v1',
          model: 'gpt-4o-mini-transcribe',
          language: '',
          vocab: '',
        ),
        readContextTerms: () => const [],
        onResult: (_) {},
      );
      addTearDown(() {
        c4.dispose();
        p4.close();
      });

      await c4.start();
      final sid4 = p4.lastSessionId!;
      final path4 = p4.lastRecordedPath!;
      final finishFuture4 = c4.finish();
      await Future<void>.delayed(Duration.zero);
      await Future<void>.delayed(Duration.zero);
      expect(c4.isTranscribing, isTrue);

      p4.emit(
        VoiceInterruptedEvent(
          sessionId: sid4,
          path: path4,
          durationMs: 3100,
          reason: 'background',
        ),
      );
      await Future<void>.delayed(Duration.zero);
      expect(capturedToken?.isCancelled, isTrue);
      expect(c4.hasFailedRetry, isTrue);

      asrGate.complete('迟到旧结果');
      await finishFuture4;
      expect(c4.hasFailedRetry, isTrue);
      expect(p4.deletedPaths, isNot(contains(path4)));
    });

    test('多控制器共用广播流：B 抢占 A 后仅 B 收到 B 的 final 事件', () async {
      final platform = _FakeVoicePlatform();
      final secrets = _MemorySecretStore();
      String? resA;
      String? resB;
      final ctrlA = VoiceController(
        platform: platform,
        secretStore: secrets,
        cloudAsr: _FakeCloudAsr(),
        readConfig: () => const VoiceConfig(
          engine: VoiceEngine.native,
          baseUrl: '',
          model: '',
          language: 'zh',
          vocab: '',
        ),
        readContextTerms: () => const [],
        onResult: (t) => resA = t,
      );
      final ctrlB = VoiceController(
        platform: platform,
        secretStore: secrets,
        cloudAsr: _FakeCloudAsr(),
        readConfig: () => const VoiceConfig(
          engine: VoiceEngine.native,
          baseUrl: '',
          model: '',
          language: 'zh',
          vocab: '',
        ),
        readContextTerms: () => const [],
        onResult: (t) => resB = t,
      );
      addTearDown(() {
        ctrlA.dispose();
        ctrlB.dispose();
        platform.close();
      });

      await ctrlA.start();
      await ctrlB.start();
      final sidB = platform.lastSessionId!;
      expect(ctrlA.isIdle, isTrue);
      expect(ctrlB.isRecording, isTrue);

      platform.emit(VoiceFinalEvent('只给 B 的文本', sessionId: sidB));
      await Future<void>.delayed(Duration.zero);
      expect(resA, isNull);
      expect(resB, '只给 B 的文本');
    });

    test('录音中 dispose 自动取消原生采集并清理临时文件', () async {
      final platform = _FakeVoicePlatform();
      final secrets = _MemorySecretStore()..map['asr_openai'] = 'sk-ok';
      final ctrl = VoiceController(
        platform: platform,
        secretStore: secrets,
        cloudAsr: _FakeCloudAsr(),
        readConfig: () => const VoiceConfig(
          engine: VoiceEngine.openai,
          baseUrl: 'https://api.openai.com/v1',
          model: 'gpt-4o-mini-transcribe',
          language: '',
          vocab: '',
        ),
        readContextTerms: () => const [],
        onResult: (_) {},
      );
      addTearDown(platform.close);

      await ctrl.start();
      final sid = platform.lastSessionId!;
      final path = platform.lastRecordedPath!;
      expect(platform.recording, isTrue);

      ctrl.dispose();
      await Future<void>.delayed(Duration.zero);
      expect(platform.recording, isFalse);
      expect(platform.cancelledSessions, contains(sid));
      expect(platform.deletedPaths, contains(path));
    });

    test('系统原生识别收尾中断时回填已有文本不卡死', () async {
      final platform = _FakeVoicePlatform();
      final secrets = _MemorySecretStore();
      String? received;
      final ctrl = VoiceController(
        platform: platform,
        secretStore: secrets,
        cloudAsr: _FakeCloudAsr(),
        readConfig: () => const VoiceConfig(
          engine: VoiceEngine.native,
          baseUrl: '',
          model: '',
          language: 'zh',
          vocab: 'Posse',
        ),
        readContextTerms: () => ['main'],
        onResult: (t) => received = t,
      );
      addTearDown(() {
        ctrl.dispose();
        platform.close();
      });

      await ctrl.start();
      final sid = platform.lastSessionId!;
      expect(platform.lastHints, containsAll(['main', 'Posse']));

      platform.emit(VoicePartialEvent('收尾前内容', sessionId: sid));
      await Future<void>.delayed(Duration.zero);
      await ctrl.finish();
      expect(ctrl.isTranscribing, isTrue);

      platform.emit(
        VoiceInterruptedEvent(
          sessionId: sid,
          text: '收尾前内容',
          durationMs: 0,
          reason: 'audio_interrupted',
        ),
      );
      await Future<void>.delayed(Duration.zero);
      expect(ctrl.isIdle, isTrue);
      expect(received, '收尾前内容');
    });
  });

  group('Widget 测试：TerminalDraftBar 与 VoiceSettingsScreen', () {
    testWidgets('TerminalDraftBar 支持输入、粘贴不回车、发送与录音变形', (tester) async {
      final platform = _FakeVoicePlatform();
      final secrets = _MemorySecretStore();
      final textCtrl = TextEditingController();
      final focus = FocusNode();
      var sent = 0;
      var pasted = 0;
      var collapsed = 0;
      double reportedHeight = 0;

      final voice = VoiceController(
        platform: platform,
        secretStore: secrets,
        cloudAsr: _FakeCloudAsr(),
        readConfig: () => const VoiceConfig(
          engine: VoiceEngine.native,
          baseUrl: '',
          model: '',
          language: '',
          vocab: '',
        ),
        readContextTerms: () => const [],
        onResult: (t) => textCtrl.text = t,
      );
      addTearDown(() {
        voice.dispose();
        textCtrl.dispose();
        focus.dispose();
        platform.close();
      });

      await tester.pumpWidget(
        _wrap(
          Align(
            alignment: Alignment.bottomCenter,
            child: TerminalDraftBar(
              controller: textCtrl,
              focusNode: focus,
              voice: voice,
              canSend: true,
              sending: false,
              onSend: () => sent++,
              onPasteOnly: () => pasted++,
              onCollapse: () => collapsed++,
              onHeightChanged: (h) => reportedHeight = h,
            ),
          ),
        ),
      );
      await tester.pump();
      expect(reportedHeight, greaterThan(0));

      await tester.enterText(
        find.byKey(const Key('terminal-draft-field')),
        'ls -la',
      );
      await tester.pump();
      await tester.tap(find.byKey(const Key('terminal-draft-paste')));
      await tester.tap(find.byKey(const Key('terminal-draft-send')));
      await tester.tap(find.byKey(const Key('terminal-draft-collapse')));
      expect(pasted, 1);
      expect(sent, 1);
      expect(collapsed, 1);

      await tester.tap(find.byKey(const Key('voice-mic-button')));
      await tester.pump();
      expect(find.byKey(const Key('voice-recording-row')), findsOneWidget);
      await tester.tap(find.byKey(const Key('voice-cancel-button')));
      await tester.pump();
      expect(find.byKey(const Key('terminal-draft-field')), findsOneWidget);
    });

    testWidgets('VoiceSettingsScreen 切换引擎、校验HTTPS与保存/清除 API Key', (
      tester,
    ) async {
      final oldStore = voiceSecretStore;
      final memStore = _MemorySecretStore();
      voiceSecretStore = memStore;
      addTearDown(() => voiceSecretStore = oldStore);

      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
      );
      addTearDown(model.dispose);

      await tester.pumpWidget(_wrap(VoiceSettingsScreen(model: model)));
      await tester.pumpAndSettle();

      await tester.tap(find.byKey(const Key('voice-engine-volcengine')));
      await tester.pumpAndSettle();
      expect(model.settings.voiceEngine, 'volcengine');
      expect(find.byKey(const Key('voice-api-key-field')), findsOneWidget);

      // 接口地址收在「高级设置」里，先展开
      await tester.ensureVisible(
        find.byKey(const Key('voice-advanced-toggle')),
      );
      await tester.tap(find.byKey(const Key('voice-advanced-toggle')));
      await tester.pumpAndSettle();

      await tester.ensureVisible(find.byKey(const Key('voice-base-url-field')));
      await tester.enterText(
        find.byKey(const Key('voice-base-url-field')),
        'http://192.168.1.9:8000/flash',
      );
      await tester.pump();
      expect(find.byKey(const Key('voice-https-warning')), findsOneWidget);

      await tester.ensureVisible(find.byKey(const Key('voice-api-key-field')));
      await tester.enterText(
        find.byKey(const Key('voice-api-key-field')),
        'volc-secret-999',
      );
      await tester.pump();
      await tester.ensureVisible(find.byKey(const Key('voice-api-key-save')));
      await tester.tap(find.byKey(const Key('voice-api-key-save')));
      await tester.pumpAndSettle();
      expect(memStore.map['asr_volcengine'], 'volc-secret-999');

      await tester.ensureVisible(find.byKey(const Key('voice-api-key-clear')));
      await tester.tap(find.byKey(const Key('voice-api-key-clear')));
      await tester.pumpAndSettle();
      expect(memStore.map.containsKey('asr_volcengine'), isFalse);
    });
  });

  group('Widget 测试：语音与草稿栏的交互细节', () {
    VoiceController nativeVoice(
      _FakeVoicePlatform platform, {
      void Function(String)? onResult,
      void Function(String)? onNotice,
    }) => VoiceController(
      platform: platform,
      secretStore: _MemorySecretStore(),
      cloudAsr: _FakeCloudAsr(),
      readConfig: () => const VoiceConfig(
        engine: VoiceEngine.native,
        baseUrl: '',
        model: '',
        language: '',
        vocab: '',
      ),
      readContextTerms: () => const [],
      onResult: onResult ?? (_) {},
      onNotice: onNotice,
    );

    Widget draftBar(
      TextEditingController text,
      FocusNode focus,
      VoiceController voice, {
      VoidCallback? onPaste,
    }) => _wrap(
      Align(
        alignment: Alignment.bottomCenter,
        child: TerminalDraftBar(
          controller: text,
          focusNode: focus,
          voice: voice,
          canSend: true,
          sending: false,
          onSend: () {},
          onPasteOnly: onPaste ?? () {},
          onCollapse: () {},
          onHeightChanged: (_) {},
        ),
      ),
    );

    test('草稿/语音键：收着时展开打字，录音中是完成，转写中不响应，其余收起', () {
      expect(
        draftKeyAction(draftOpen: false, stage: VoiceStage.idle),
        DraftKeyAction.open,
      );
      expect(
        draftKeyAction(draftOpen: false, stage: VoiceStage.failedRetryable),
        DraftKeyAction.open,
      );
      expect(
        draftKeyAction(draftOpen: true, stage: VoiceStage.recording),
        DraftKeyAction.finish,
      );
      expect(
        draftKeyAction(draftOpen: true, stage: VoiceStage.stopping),
        DraftKeyAction.ignore,
      );
      expect(
        draftKeyAction(draftOpen: true, stage: VoiceStage.transcribing),
        DraftKeyAction.ignore,
      );
      expect(
        draftKeyAction(draftOpen: true, stage: VoiceStage.starting),
        DraftKeyAction.collapse,
      );
      expect(
        draftKeyAction(draftOpen: true, stage: VoiceStage.idle),
        DraftKeyAction.collapse,
      );
    });

    testWidgets('连点两下完成：第二下落在转圈上，不会取消转写', (tester) async {
      final platform = _FakeVoicePlatform();
      final notices = <String>[];
      final voice = nativeVoice(platform, onNotice: notices.add);
      final text = TextEditingController();
      final focus = FocusNode();
      addTearDown(() {
        voice.dispose();
        text.dispose();
        focus.dispose();
        platform.close();
      });
      await tester.pumpWidget(draftBar(text, focus, voice));
      await tester.tap(find.byKey(const Key('voice-mic-button')));
      await tester.pump();
      expect(voice.isRecording, isTrue);

      final finish = find.byKey(const Key('voice-finish-button'));
      final spot = tester.getCenter(finish);
      final cancelSpot = tester.getCenter(
        find.byKey(const Key('voice-cancel-button')),
      );
      await tester.tapAt(spot);
      await tester.pump();
      expect(voice.isTranscribing, isTrue);
      expect(find.byKey(const Key('voice-transcribing-row')), findsOneWidget);
      // 完成和取消的位置在转写时不变
      expect(tester.getCenter(finish), spot);
      expect(
        tester.getCenter(find.byKey(const Key('voice-cancel-button'))),
        cancelSpot,
      );

      await tester.tapAt(spot);
      await tester.pump();
      expect(voice.isTranscribing, isTrue);
      expect(platform.cancelledSessions, isEmpty);

      // 本机识别收尾兜底到期后回到空闲
      await tester.pump(const Duration(milliseconds: 1600));
      expect(voice.isIdle, isTrue);
    });

    testWidgets('启动中先画成未亮起的录音行，不闪「正在转写」，完成不可点', (tester) async {
      final platform = _FakeVoicePlatform()..delayedStart = Completer<void>();
      final secrets = _MemorySecretStore()..map['asr_openai'] = 'sk-test';
      final voice = VoiceController(
        platform: platform,
        secretStore: secrets,
        cloudAsr: _FakeCloudAsr(),
        readConfig: () => VoiceConfig(
          engine: VoiceEngine.openai,
          baseUrl: VoiceEngine.openai.defaultBaseUrl,
          model: VoiceEngine.openai.defaultModel,
          language: '',
          vocab: '',
        ),
        readContextTerms: () => const [],
        onResult: (_) {},
      );
      addTearDown(() {
        voice.dispose();
        platform.close();
      });
      await tester.pumpWidget(_wrap(VoiceActiveRow(controller: voice)));
      unawaited(voice.start());
      await tester.pump();
      await tester.pump();
      expect(voice.isStarting, isTrue);
      expect(find.byKey(const Key('voice-recording-row')), findsOneWidget);
      expect(find.text('正在转写…'), findsNothing);
      await tester.tap(find.byKey(const Key('voice-finish-button')));
      await tester.pump();
      expect(voice.isStarting, isTrue);

      platform.delayedStart!.complete();
      await tester.pump();
      await tester.pump();
      expect(voice.isRecording, isTrue);
      await voice.cancel();
      await tester.pump();
    });

    testWidgets('多行草稿时「填入」先变淡并提示原因，点了仍交给页面拦截', (tester) async {
      final platform = _FakeVoicePlatform();
      final voice = nativeVoice(platform);
      final text = TextEditingController();
      final focus = FocusNode();
      var pasted = 0;
      addTearDown(() {
        voice.dispose();
        text.dispose();
        focus.dispose();
        platform.close();
      });
      await tester.pumpWidget(
        draftBar(text, focus, voice, onPaste: () => pasted++),
      );
      final c = PosseColors.darkColors;
      Icon pasteIcon() => tester.widget<Icon>(
        find.descendant(
          of: find.byKey(const Key('terminal-draft-paste')),
          matching: find.byType(Icon),
        ),
      );

      text.text = 'echo ok';
      await tester.pump();
      expect(pasteIcon().color, c.muted);
      expect(find.byTooltip('填入终端（不回车）'), findsOneWidget);

      text.text = 'echo a\necho b';
      await tester.pump();
      expect(pasteIcon().color, c.faint);
      expect(find.byTooltip('「仅填入」只支持单行纯文本；多行或含控制字符请改用「发送」'), findsOneWidget);
      await tester.tap(find.byKey(const Key('terminal-draft-paste')));
      expect(pasted, 1);
    });

    testWidgets('草稿框关闭智能引号、破折号和自动更正', (tester) async {
      final platform = _FakeVoicePlatform();
      final voice = nativeVoice(platform);
      final text = TextEditingController();
      final focus = FocusNode();
      addTearDown(() {
        voice.dispose();
        text.dispose();
        focus.dispose();
        platform.close();
      });
      await tester.pumpWidget(draftBar(text, focus, voice));
      final field = tester.widget<TextField>(
        find.byKey(const Key('terminal-draft-field')),
      );
      expect(field.smartQuotesType, SmartQuotesType.disabled);
      expect(field.smartDashesType, SmartDashesType.disabled);
      expect(field.autocorrect, isFalse);
    });

    testWidgets('录音行和输入行同高，切换时栏高不变', (tester) async {
      final platform = _FakeVoicePlatform();
      final voice = nativeVoice(platform);
      final text = TextEditingController();
      final focus = FocusNode();
      addTearDown(() {
        voice.dispose();
        text.dispose();
        focus.dispose();
        platform.close();
      });
      await tester.pumpWidget(draftBar(text, focus, voice));
      final idle = tester
          .getSize(find.byKey(const Key('terminal-draft-bar')))
          .height;
      await tester.tap(find.byKey(const Key('voice-mic-button')));
      await tester.pump();
      expect(voice.isRecording, isTrue);
      expect(
        tester.getSize(find.byKey(const Key('terminal-draft-bar'))).height,
        idle,
      );
      await voice.cancel();
      await tester.pump();
    });

    testWidgets('实时预览在本机识别收尾时仍然显示', (tester) async {
      final platform = _FakeVoicePlatform();
      final voice = nativeVoice(platform);
      final text = TextEditingController();
      final focus = FocusNode();
      addTearDown(() {
        voice.dispose();
        text.dispose();
        focus.dispose();
        platform.close();
      });
      await tester.pumpWidget(draftBar(text, focus, voice));
      await tester.tap(find.byKey(const Key('voice-mic-button')));
      await tester.pump();
      platform.emit(
        VoicePartialEvent('你好世界', sessionId: platform.lastSessionId!),
      );
      await tester.pump();
      expect(find.byKey(const Key('voice-partial-preview')), findsOneWidget);
      await tester.tap(find.byKey(const Key('voice-finish-button')));
      await tester.pump();
      expect(voice.isTranscribing, isTrue);
      expect(find.text('你好世界'), findsOneWidget);
      await tester.pump(const Duration(milliseconds: 1600));
    });

    testWidgets('快捷键栏：收起时有草稿显示强调色圆点，有待重试录音显示琥珀色', (tester) async {
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
      );
      addTearDown(model.dispose);
      Future<void> pump({
        bool open = false,
        bool draft = false,
        bool pending = false,
      }) => tester.pumpWidget(
        _wrap(
          Align(
            alignment: Alignment.bottomCenter,
            child: TerminalKeyBar(
              model: model,
              onKey: (_) async {},
              onCustom: (_) {},
              onExit: () {},
              onClear: () {},
              onImage: () {},
              onPaste: () {},
              keyboardUp: false,
              onKeyboard: () {},
              onVoice: () {},
              draftOpen: open,
              hasDraft: draft,
              voicePending: pending,
            ),
          ),
        ),
      );
      final dot = find.byKey(const Key('terminal-key-voice-dot'));
      Color dotColor() =>
          (tester.widget<Container>(dot).decoration! as BoxDecoration).color!;

      await pump();
      expect(dot, findsNothing);
      await pump(draft: true);
      expect(dot, findsOneWidget);
      expect(dotColor(), PosseColors.darkColors.accent);
      expect(tester.getSize(dot).width, greaterThan(8));
      expect(find.bySemanticsLabel('草稿 / 语音，有未发送的草稿'), findsOneWidget);
      await pump(pending: true);
      expect(dotColor(), PosseColors.darkColors.honey);
      await pump(open: true, draft: true);
      expect(dot, findsNothing);
    });

    test('设置首页摘要：本机只写名字，云端写 Key 状态，未读到时只写引擎名', () async {
      final l = await AppLocalizations.delegate.load(const Locale('zh'));
      expect(voiceSummaryText(l, VoiceEngine.native, null), '系统本机识别');
      expect(voiceSummaryText(l, VoiceEngine.openai, null), 'OpenAI');
      expect(voiceSummaryText(l, VoiceEngine.openai, true), 'OpenAI · 已配置 Key');
      expect(
        voiceSummaryText(l, VoiceEngine.openai, false),
        'OpenAI · 需配置 API Key',
      );
    });

    testWidgets('设置首页「语音输入」：缺 Key 时提示，配好回来后更新', (tester) async {
      final oldStore = voiceSecretStore;
      final memStore = _MemorySecretStore();
      voiceSecretStore = memStore;
      addTearDown(() => voiceSecretStore = oldStore);
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
      );
      addTearDown(model.dispose);
      model.updateSettings((s) => s.voiceEngine = 'openai');

      await tester.pumpWidget(
        MaterialApp(
          locale: const Locale('zh'),
          localizationsDelegates: AppLocalizations.localizationsDelegates,
          supportedLocales: AppLocalizations.supportedLocales,
          theme: posseTheme(Brightness.dark),
          home: SettingsScreen(model: model, onComputers: () {}),
        ),
      );
      await tester.pumpAndSettle();
      expect(find.text('OpenAI · 需配置 API Key'), findsOneWidget);

      memStore.map['asr_openai'] = 'sk-x';
      final entry = find.byKey(const Key('settings-voice-entry'));
      await tester.ensureVisible(entry);
      await tester.tap(entry);
      await tester.pumpAndSettle();
      expect(find.byType(VoiceSettingsScreen), findsOneWidget);
      expect(find.byKey(const Key('voice-key-state')), findsOneWidget);
      expect(find.text('已配置 Key'), findsWidgets);
      tester.state<NavigatorState>(find.byType(Navigator)).pop();
      await tester.pumpAndSettle();
      expect(find.text('OpenAI · 已配置 Key'), findsOneWidget);
    });

    testWidgets('自定义引擎地址放在外面并提示必填，高级设置默认收起', (tester) async {
      final oldStore = voiceSecretStore;
      voiceSecretStore = _MemorySecretStore();
      addTearDown(() => voiceSecretStore = oldStore);
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
      );
      addTearDown(model.dispose);
      model.updateSettings((s) => s.voiceEngine = 'custom');
      await tester.pumpWidget(_wrap(VoiceSettingsScreen(model: model)));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('voice-url-required')), findsOneWidget);
      await tester.ensureVisible(find.byKey(const Key('voice-base-url-field')));
      expect(
        find.byKey(const Key('voice-base-url-field')).hitTestable(),
        findsOneWidget,
      );
      expect(
        find.byKey(const Key('voice-model-field')).hitTestable(),
        findsNothing,
      );
      await tester.ensureVisible(
        find.byKey(const Key('voice-advanced-toggle')),
      );
      await tester.tap(find.byKey(const Key('voice-advanced-toggle')));
      await tester.pumpAndSettle();
      await tester.ensureVisible(find.byKey(const Key('voice-model-field')));
      expect(
        find.byKey(const Key('voice-model-field')).hitTestable(),
        findsOneWidget,
      );
    });
  });
}
