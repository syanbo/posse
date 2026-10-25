import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/pair/scan.dart';

void main() {
  test('a pairing url or raw code connects once; other codes do not', () {
    final code = _pairCode();
    final url = 'orca://pair?code=$code';
    expect(_run(url), [url]);
    expect(_run('  $url  '), [url]);
    expect(_run(code), [code]);

    expect(_run('https://example.com'), isEmpty);
    expect(_run('not a pairing code'), isEmpty);
    expect(_run(''), isEmpty);

    final watched = _watch();
    watched.onCode('https://example.com?code=nope');
    watched.onCode('orca://pair?code=$code');
    watched.onCode('orca://pair?code=$code');
    watched.onCode(_pairCode(token: 'other'));
    expect(watched.connected, ['orca://pair?code=$code']);
    expect(watched.stops, 1);
  });
}

List<String> _run(String raw) {
  final watched = _watch();
  watched.onCode(raw);
  expect(watched.stops, watched.connected.isEmpty ? 0 : 1);
  return watched.connected;
}

_Watch _watch() => _Watch();

class _Watch {
  _Watch() {
    session = ScanSession(stop: () => stops++, connect: connected.add);
  }

  late final ScanSession session;
  final connected = <String>[];
  var stops = 0;

  void onCode(String raw) => session.onCode(raw);
}

String _pairCode({String token = 'tok'}) {
  final payload = {
    'v': 2,
    'endpoint': 'ws://127.0.0.1:6788',
    'endpoints': ['ws://127.0.0.1:6788'],
    'deviceToken': token,
    'publicKeyB64': 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=',
    'pairedDeviceId': 'dev',
    'scope': 'mobile',
  };
  return base64Url.encode(utf8.encode(jsonEncode(payload))).replaceAll('=', '');
}
