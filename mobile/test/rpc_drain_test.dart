import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/chat/image_upload.dart';
import 'package:posse_mobile/protocol/e2ee.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/protocol/socket.dart';

class _Socket implements ByteSocket {
  final incoming = StreamController<SocketEvent>();
  final sent = StreamController<SocketEvent>();
  var closed = false;

  @override
  Stream<SocketEvent> get events => incoming.stream;
  @override
  Future<void> sendText(String text) async =>
      sent.add(SocketEvent.text(utf8.encode(text)));
  @override
  Future<void> sendBinary(Uint8List bytes) async =>
      sent.add(SocketEvent.binary(bytes));
  @override
  Future<void> close() async => closed = true;
  @override
  int? get closeCode => null;
}

Uint8List _key(int seed) => Uint8List.fromList(List.filled(32, seed));

/// 一条真实的 PhoneRpc，对面是照电脑协议回话的替身：开始上传回编号、收分段（确认由测试放）、提交回路径。
class _Mac {
  _Mac() {
    rpc = PhoneRpc(
      socket: socket,
      session: desk.asPhone(),
      deviceToken: 't',
      reader: SocketReader(socket.events),
    )..start();
    socket.sent.stream.listen(_onFrame);
  }

  final desk = E2eeSession(
    recvKey: _key(1),
    sendKey: _key(2),
    id: Uint8List(32),
    transcriptHash: Uint8List(32),
  );
  final socket = _Socket();
  late final PhoneRpc rpc;
  var received = 0;
  var commits = 0;

  void _push(Uint8List plain, {required bool binary}) {
    socket.incoming.add(
      SocketEvent.binary(
        desk.seal(
          plain,
          direction: dirDesktopToMobile,
          kind: binary ? kindBinary : kindText,
        ),
      ),
    );
  }

  void _reply(Object? id, Map<String, Object?> result) => _push(
    Uint8List.fromList(
      utf8.encode(jsonEncode({'id': id, 'ok': true, 'result': result})),
    ),
    binary: false,
  );

  void _onFrame(SocketEvent event) {
    final (kind, plain) = desk.openMessage(
      event.bytes,
      binary: event.binary,
      direction: dirMobileToDesktop,
    );
    if (kind == kindBinary) {
      received += plain.length - bulkHeader;
      return;
    }
    final request = jsonDecode(utf8.decode(plain)) as Map;
    switch (request['method']) {
      case 'clipboard.startImageUpload':
        _reply(request['id'], {'uploadId': 'u1', 'blob': 5, 'ack': true});
      case 'clipboard.saveImageAsTempFile':
        _reply(request['id'], {'value': '/tmp/posse-small.png'});
      case 'clipboard.commitImageUpload':
        commits++;
        _reply(request['id'], {'value': '/tmp/posse-up.png'});
    }
  }

  /// 确认到现在收下的全部字节
  void ackAll() => _push(
    bulkFrame(
      opUploadAck,
      5,
      Uint8List(8)..buffer.asByteData().setUint64(0, received, Endian.little),
    ),
    binary: true,
  );
}

void main() {
  test(
    'a draining connection waits for its upload past max, then closes',
    () async {
      final mac = _Mac();
      final bytes = List.filled(6 * bulkChunk, 7);
      final up = uploadImage(mac.rpc, bytes);
      await pumpEventQueue();
      expect(mac.rpc.uploading, 1);
      // 中途换到更好的连接：旧连接开始排空，上限很短
      final closing = mac.rpc.closeWhenIdle(
        min: const Duration(milliseconds: 10),
        max: const Duration(milliseconds: 50),
      );
      await Future<void>.delayed(const Duration(milliseconds: 300));
      expect(
        mac.socket.closed,
        isFalse,
        reason: 'closed under a running upload',
      );
      // 排空中不接新的上传和订阅
      await expectLater(
        uploadImage(mac.rpc, bytes),
        throwsA(isA<RpcException>().having((e) => e.code, 'code', 'closed')),
      );
      await expectLater(
        mac.rpc.subscribe('terminal.subscribe', const {}),
        throwsA(isA<RpcException>().having((e) => e.code, 'code', 'closed')),
      );
      // 电脑确认收下，上传在旧连接上传完
      while (mac.received < bytes.length) {
        mac.ackAll();
        await Future<void>.delayed(const Duration(milliseconds: 10));
      }
      mac.ackAll();
      expect(await up, '/tmp/posse-up.png');
      expect(mac.commits, 1);
      await closing;
      expect(mac.socket.closed, isTrue);
      expect(mac.rpc.uploading, 0);
    },
  );

  test('the draining connection drops mid-upload: the upload fails and the count returns to zero', () async {
    final mac = _Mac();
    final up = uploadImage(mac.rpc, List.filled(6 * bulkChunk, 7));
    final failed = expectLater(
      up,
      throwsA(isA<RpcException>().having((e) => e.code, 'code', 'closed')),
    );
    await pumpEventQueue();
    final closing = mac.rpc.closeWhenIdle(
      min: const Duration(milliseconds: 10),
      max: const Duration(milliseconds: 50),
    );
    await Future<void>.delayed(const Duration(milliseconds: 100));
    // 过了上限还开着（上传在跑）；是连接自己断了，不是排空到点关掉的
    expect(mac.socket.closed, isFalse, reason: 'closed under a running upload');
    expect(mac.rpc.uploading, 1);
    // 旧连接自己断了
    await mac.socket.incoming.close();
    await failed;
    await closing;
    expect(mac.rpc.uploading, 0);
  });

  test('an upload refused by a draining connection restarts once on the current one', () async {
    final old = _Mac();
    final next = _Mac();
    var current = old.rpc;
    // 旧连接开始排空、还没换下来时 model.rpc 已经是新的
    unawaited(
      old.rpc.closeWhenIdle(
        min: const Duration(milliseconds: 10),
        max: const Duration(milliseconds: 10),
      ),
    );
    var tried = <PhoneRpc>[];
    final path = await uploadOnCurrent(
      () {
        final rpc = current;
        current = next.rpc;
        return rpc;
      },
      (rpc) {
        tried.add(rpc);
        return uploadImage(rpc, [1, 2, 3]);
      },
    );
    expect(tried, [old.rpc, next.rpc]);
    expect(path, '/tmp/posse-small.png');

    // 传到一半断开的不自动重传
    tried = [];
    await expectLater(
      uploadOnCurrent(() => next.rpc, (rpc) {
        tried.add(rpc);
        throw RpcException('closed', 'connection closed');
      }),
      throwsA(isA<RpcException>()),
    );
    expect(tried, hasLength(1));
    await next.rpc.close();
  });
}
