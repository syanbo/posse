import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:math';
import 'dart:typed_data';

import 'voice_config.dart';

/// 语音转写异常，使用结构化错误码以便按界面语言翻译。
class VoiceAsrException implements Exception {
  const VoiceAsrException(this.code, [this.detail = '']);

  /// 错误码：`missing_api_key` | `invalid_url` | `auth_failed` | `rate_limited` |
  /// `network` | `timeout` | `cancelled` | `empty_result` | `server_error`
  final String code;
  final String detail;

  @override
  String toString() =>
      'VoiceAsrException($code${detail.isEmpty ? '' : ': $detail'})';
}

/// 取消令牌：中止正在进行的 HTTP 转写请求。
class VoiceCancelToken {
  bool _cancelled = false;
  final _listeners = <void Function()>[];

  bool get isCancelled => _cancelled;

  void onCancel(void Function() fn) {
    if (_cancelled) {
      fn();
    } else {
      _listeners.add(fn);
    }
  }

  void cancel() {
    if (_cancelled) return;
    _cancelled = true;
    for (final fn in List<void Function()>.of(_listeners)) {
      fn();
    }
    _listeners.clear();
  }
}

/// 云端 ASR 转写接口（便于单元测试注入 mock）。
abstract class CloudAsrClient {
  Future<String> transcribe({
    required VoiceConfig config,
    required String apiKey,
    required String filePath,
    required List<String> contextTerms,
    VoiceCancelToken? cancelToken,
  });
}

/// 默认云端 ASR 实现：支持 OpenAI 兼容 `/v1/audio/transcriptions` 与火山豆包录音极速版 HTTP。
class HttpCloudAsrClient implements CloudAsrClient {
  HttpCloudAsrClient({
    HttpClient Function()? clientFactory,
    this.timeout = const Duration(seconds: 45),
    this.allowInsecureHttpForTest = false,
  }) : _clientFactory = clientFactory ?? HttpClient.new;

  final HttpClient Function() _clientFactory;
  final Duration timeout;
  final bool allowInsecureHttpForTest;

  @override
  Future<String> transcribe({
    required VoiceConfig config,
    required String apiKey,
    required String filePath,
    required List<String> contextTerms,
    VoiceCancelToken? cancelToken,
  }) async {
    if (apiKey.trim().isEmpty) {
      throw const VoiceAsrException('missing_api_key');
    }
    final file = File(filePath);
    if (!await file.exists()) {
      throw const VoiceAsrException('empty_result', 'audio file missing');
    }
    final bytes = await file.readAsBytes();
    if (bytes.isEmpty) {
      throw const VoiceAsrException('empty_result', 'audio file empty');
    }

    if (config.engine == VoiceEngine.volcengine) {
      return _transcribeVolcengine(
        config: config,
        apiKey: apiKey.trim(),
        bytes: bytes,
        cancelToken: cancelToken,
      );
    }
    return _transcribeOpenAICompatible(
      config: config,
      apiKey: apiKey.trim(),
      bytes: bytes,
      fileName: file.uri.pathSegments.isEmpty
          ? 'voice.${config.engine.recordFormat}'
          : file.uri.pathSegments.last,
      contextTerms: contextTerms,
      cancelToken: cancelToken,
    );
  }

  bool _allowHttp(VoiceConfig config) =>
      allowInsecureHttpForTest || config.allowInsecureHttp;

  /// OpenAI 兼容 `POST <baseUrl>/audio/transcriptions`（multipart/form-data）。
  Future<String> _transcribeOpenAICompatible({
    required VoiceConfig config,
    required String apiKey,
    required Uint8List bytes,
    required String fileName,
    required List<String> contextTerms,
    VoiceCancelToken? cancelToken,
  }) async {
    final uri = resolveOpenAITranscriptionUri(
      config.baseUrl,
      allowInsecureHttp: _allowHttp(config),
    );
    if (uri == null) {
      throw const VoiceAsrException('invalid_url');
    }

    final boundary = '----PosseVoice${DateTime.now().microsecondsSinceEpoch}';
    final fields = <String, String>{
      'model': config.model.isNotEmpty
          ? config.model
          : config.engine.defaultModel,
    };
    if (config.language.isNotEmpty) {
      fields['language'] = config.language;
    }
    if (config.engine.supportsPrompt) {
      final prompt = VoicePromptHints.buildOpenAIPrompt(
        vocab: config.vocab,
        contextTerms: contextTerms,
      );
      if (prompt.isNotEmpty) {
        fields['prompt'] = prompt;
      }
    }

    final contentType = fileName.toLowerCase().endsWith('.wav')
        ? 'audio/wav'
        : 'audio/mp4';
    final body = buildMultipartBody(
      boundary: boundary,
      fields: fields,
      fileField: 'file',
      fileName: fileName,
      fileContentType: contentType,
      fileBytes: bytes,
    );

    final resp = await _sendPost(
      uri: uri,
      headers: {
        HttpHeaders.authorizationHeader: 'Bearer $apiKey',
        HttpHeaders.contentTypeHeader: 'multipart/form-data; boundary=$boundary',
      },
      body: body,
      cancelToken: cancelToken,
    );

    final text = parseOpenAITranscriptionResponse(resp.body);
    if (text.isEmpty) {
      throw const VoiceAsrException('empty_result');
    }
    return text;
  }

  /// 火山引擎豆包大模型录音文件极速版 HTTP。
  Future<String> _transcribeVolcengine({
    required VoiceConfig config,
    required String apiKey,
    required Uint8List bytes,
    VoiceCancelToken? cancelToken,
  }) async {
    final uri = parseVoiceEndpointUri(
      config.baseUrl,
      allowInsecureHttp: _allowHttp(config),
    );
    if (uri == null) {
      throw const VoiceAsrException('invalid_url');
    }
    final resourceId = config.model.isNotEmpty
        ? config.model
        : VoiceEngine.volcengine.defaultModel;
    final payload = jsonEncode({
      'user': {'uid': 'posse-mobile'},
      'audio': {'data': base64Encode(bytes)},
      'request': {
        'model_name': 'bigmodel',
        'enable_itn': true,
        'enable_punc': true,
      },
    });
    final reqId = _uuidV4();
    final resp = await _sendPost(
      uri: uri,
      headers: {
        HttpHeaders.contentTypeHeader: 'application/json; charset=utf-8',
        'X-Api-Key': apiKey,
        'X-Api-Resource-Id': resourceId,
        'X-Api-Request-Id': reqId,
        'X-Api-Sequence': '-1',
      },
      body: utf8.encode(payload),
      cancelToken: cancelToken,
    );

    // 豆包极速版 HTTP 200 下必须通过 X-Api-Status-Code 返回明确业务结果；
    // 缺失状态头或网关错误页均按可重试服务错误保留音频，不误删。
    final apiCode = resp.headers['x-api-status-code']?.trim() ?? '';
    final logId = resp.headers['x-tt-logid']?.trim() ?? reqId;
    if (apiCode.isEmpty) {
      throw VoiceAsrException('server_error', 'missing status header log=$logId');
    }
    if (apiCode != '20000000') {
      if (apiCode == '20000003') {
        throw VoiceAsrException('empty_result', 'code=$apiCode log=$logId');
      }
      if (apiCode == '45000010' ||
          apiCode == '45000011' ||
          apiCode == '45000012' ||
          apiCode == '401' ||
          apiCode == '403') {
        throw VoiceAsrException('auth_failed', 'code=$apiCode log=$logId');
      }
      if (apiCode == '45000029' || apiCode == '429' || apiCode == '55000031') {
        throw VoiceAsrException('rate_limited', 'code=$apiCode log=$logId');
      }
      throw VoiceAsrException('server_error', 'code=$apiCode log=$logId');
    }

    final parsed = parseVolcengineFlashResult(resp.body);
    if (!parsed.valid) {
      throw VoiceAsrException('server_error', 'invalid body log=$logId');
    }
    if (parsed.text.isEmpty) {
      throw const VoiceAsrException('empty_result');
    }
    return parsed.text;
  }

  Future<({String body, Map<String, String> headers})> _sendPost({
    required Uri uri,
    required Map<String, String> headers,
    required List<int> body,
    VoiceCancelToken? cancelToken,
  }) async {
    if (cancelToken?.isCancelled == true) {
      throw const VoiceAsrException('cancelled');
    }
    final client = _clientFactory();
    client.connectionTimeout = const Duration(seconds: 15);
    // 局域网明文 HTTP 明确禁用环境代理，避免把未加密的 Key 与音频经 http_proxy 转发出去。
    if (uri.scheme == 'http') {
      client.findProxy = (_) => 'DIRECT';
    }
    var aborted = false;
    cancelToken?.onCancel(() {
      aborted = true;
      client.close(force: true);
    });
    try {
      final req = await client.postUrl(uri).timeout(timeout);
      // 严禁自动跟随 3xx 重定向，防止把 Authorization / X-Api-Key 转发到明文或第三方地址。
      req.followRedirects = false;
      req.maxRedirects = 0;
      headers.forEach(req.headers.set);
      req.contentLength = body.length;
      req.add(body);
      final resp = await req.close().timeout(timeout);
      final text = await utf8.decodeStream(resp).timeout(timeout);
      if (aborted || cancelToken?.isCancelled == true) {
        throw const VoiceAsrException('cancelled');
      }
      if (resp.statusCode == 401 || resp.statusCode == 403) {
        throw VoiceAsrException('auth_failed', 'HTTP ${resp.statusCode}');
      }
      if (resp.statusCode == 429) {
        throw const VoiceAsrException('rate_limited', 'HTTP 429');
      }
      if (resp.statusCode < 200 || resp.statusCode >= 300) {
        throw VoiceAsrException('server_error', 'HTTP ${resp.statusCode}');
      }
      final respHeaders = <String, String>{};
      resp.headers.forEach((name, values) {
        if (values.isNotEmpty) {
          respHeaders[name.toLowerCase()] = values.first;
        }
      });
      return (body: text, headers: respHeaders);
    } on VoiceAsrException {
      rethrow;
    } on TimeoutException {
      if (aborted) throw const VoiceAsrException('cancelled');
      throw const VoiceAsrException('timeout');
    } on SocketException catch (e) {
      if (aborted) throw const VoiceAsrException('cancelled');
      throw VoiceAsrException('network', e.message);
    } on HttpException catch (e) {
      if (aborted) throw const VoiceAsrException('cancelled');
      throw VoiceAsrException('network', e.message);
    } catch (e) {
      if (aborted) throw const VoiceAsrException('cancelled');
      throw VoiceAsrException('server_error', e.toString());
    } finally {
      client.close(force: true);
    }
  }
}

final _rng = Random.secure();

String _uuidV4() {
  final bytes = List<int>.generate(16, (_) => _rng.nextInt(256));
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  String hex(int from, int to) =>
      bytes.sublist(from, to).map((b) => b.toRadixString(16).padLeft(2, '0')).join();
  return '${hex(0, 4)}-${hex(4, 6)}-${hex(6, 8)}-${hex(8, 10)}-${hex(10, 16)}';
}

/// 规范化 OpenAI 兼容接口地址：默认强制 HTTPS，兼容传入 `https://host/v1` 或完整 `.../audio/transcriptions`。
Uri? resolveOpenAITranscriptionUri(
  String rawBaseUrl, {
  bool allowInsecureHttp = false,
}) {
  final uri = parseVoiceEndpointUri(
    rawBaseUrl,
    allowInsecureHttp: allowInsecureHttp,
  );
  if (uri == null) return null;
  final path = uri.path.endsWith('/')
      ? uri.path.substring(0, uri.path.length - 1)
      : uri.path;
  if (path.endsWith('/audio/transcriptions')) {
    return uri;
  }
  return uri.replace(path: '$path/audio/transcriptions');
}

/// 构造 multipart/form-data 请求体。
Uint8List buildMultipartBody({
  required String boundary,
  required Map<String, String> fields,
  required String fileField,
  required String fileName,
  required String fileContentType,
  required List<int> fileBytes,
}) {
  final out = BytesBuilder(copy: false);
  void writeStr(String s) => out.add(utf8.encode(s));

  for (final entry in fields.entries) {
    writeStr('--$boundary\r\n');
    writeStr('Content-Disposition: form-data; name="${entry.key}"\r\n\r\n');
    writeStr('${entry.value}\r\n');
  }
  final safeName = fileName.replaceAll('"', '_');
  writeStr('--$boundary\r\n');
  writeStr(
    'Content-Disposition: form-data; name="$fileField"; filename="$safeName"\r\n',
  );
  writeStr('Content-Type: $fileContentType\r\n\r\n');
  out.add(fileBytes);
  writeStr('\r\n--$boundary--\r\n');
  return out.takeBytes();
}

/// 解析 OpenAI 兼容转写响应（JSON `{"text": "..."}` 或纯文本）。
String parseOpenAITranscriptionResponse(String raw) {
  final trimmed = raw.trim();
  if (trimmed.isEmpty) return '';
  try {
    final decoded = jsonDecode(trimmed);
    if (decoded is Map && decoded['text'] is String) {
      return (decoded['text'] as String).trim();
    }
  } catch (_) {}
  return trimmed;
}

/// 解析火山引擎豆包录音极速版响应：区分结构有效但无字（empty）与结构异常（invalid）。
({bool valid, String text}) parseVolcengineFlashResult(String raw) {
  final trimmed = raw.trim();
  if (trimmed.isEmpty) return (valid: false, text: '');
  try {
    final decoded = jsonDecode(trimmed);
    if (decoded is Map) {
      final result = decoded['result'];
      if (result is Map) {
        final text = result['text'];
        if (text == null || text is String) {
          return (valid: true, text: (text as String? ?? '').trim());
        }
      }
    }
  } catch (_) {}
  return (valid: false, text: '');
}

/// 解析火山引擎豆包录音极速版响应文本（便捷包装）。
String parseVolcengineFlashResponse(String raw) =>
    parseVolcengineFlashResult(raw).text;

/// 进程级云端 ASR 客户端（测试可替换）。
CloudAsrClient voiceCloudAsr = HttpCloudAsrClient();
