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

  @override
  Stream<SocketEvent> get events => incoming.stream;
  @override
  Future<void> sendText(String text) async =>
      sent.add(SocketEvent.text(utf8.encode(text)));
  @override
  Future<void> sendBinary(Uint8List bytes) async =>
      sent.add(SocketEvent.binary(bytes));
  @override
  Future<void> close() async {}
  @override
  int? get closeCode => null;
}

Uint8List _key(int seed) => Uint8List.fromList(List.filled(32, seed));

void main() {
  test(
    'blob chunks fill the reply fields, terminal frames still stream',
    () async {
      final desk = E2eeSession(
        recvKey: _key(1),
        sendKey: _key(2),
        id: Uint8List(32),
        transcriptHash: Uint8List(32),
      );
      final socket = _Socket();
      final rpc = PhoneRpc(
        socket: socket,
        session: desk.asPhone(),
        deviceToken: 't',
        reader: SocketReader(socket.events),
      )..start();
      final sent = StreamIterator(socket.sent.stream);
      void push(Uint8List plain, {required bool binary}) {
        final frame = desk.seal(
          plain,
          direction: dirDesktopToMobile,
          kind: binary ? kindBinary : kindText,
        );
        socket.incoming.add(SocketEvent.binary(frame));
      }

      final terminal = rpc.binary.first;
      final reply = rpc.call('git.diff', const {});
      await sent.moveNext();
      expect(sent.current.binary, isTrue);
      final (kind, requestBytes) = desk.openMessage(
        sent.current.bytes,
        binary: sent.current.binary,
        direction: dirMobileToDesktop,
      );
      expect(kind, kindText);
      final request = jsonDecode(utf8.decode(requestBytes)) as Map;
      final content = List.generate(bulkChunk + 10, (i) => i % 251);
      push(
        bulkFrame(opBlobChunk, 7, content.sublist(0, bulkChunk)),
        binary: true,
      );
      push(Uint8List.fromList([0x74, 1, 1, 0, 9, 9]), binary: true);
      push(bulkFrame(opBlobChunk, 8, [5, 6]), binary: true);
      push(bulkFrame(opBlobChunk, 7, content.sublist(bulkChunk)), binary: true);
      push(
        Uint8List.fromList(
          utf8.encode(
            jsonEncode({
              'id': request['id'],
              'ok': true,
              'result': {
                'size': content.length,
                'images': {
                  'new': {'size': 2},
                },
                'blobs': {'content': 7, 'images.new.content': 8},
              },
            }),
          ),
        ),
        binary: false,
      );
      final result = await reply;
      expect(result.containsKey('blobs'), isFalse);
      expect(rpcBytes(result['content']), content);
      final images = result['images'] as Map;
      expect(rpcBytes((images['new'] as Map)['content']), [5, 6]);
      expect((await terminal).first, 0x74);
      // 旧电脑的 base64 照样认
      expect(rpcBytes(base64.encode([1, 2])), [1, 2]);
      expect(rpcBytes(null), isEmpty);

      // 上传的分段是同样格式的二进制帧
      unawaited(rpc.sendUploadChunk(3, [9, 8, 7]));
      await sent.moveNext();
      expect(sent.current.binary, isTrue);
      final chunk = desk.open(
        sent.current.bytes,
        direction: dirMobileToDesktop,
        kind: kindBinary,
      );
      expect(chunk.sublist(0, 4), [bulkMagic, 1, opUploadChunk, 0]);
      expect(ByteData.sublistView(chunk).getUint32(4, Endian.little), 3);
      expect(chunk.sublist(bulkHeader), [9, 8, 7]);
      await rpc.close();
    },
  );

  test(
    'upload waits for acks and shrinks the window when acks queue up',
    () async {
      final desk = E2eeSession(
        recvKey: _key(1),
        sendKey: _key(2),
        id: Uint8List(32),
        transcriptHash: Uint8List(32),
      );
      final socket = _Socket();
      final rpc = PhoneRpc(
        socket: socket,
        session: desk.asPhone(),
        deviceToken: 't',
        reader: SocketReader(socket.events),
      )..start();
      final frames = <SocketEvent>[];
      socket.sent.stream.listen(frames.add);
      final sender = UploadSender(rpc, 5, acked: true);
      final chunk = List.filled(bulkChunk, 1);
      var done = 0;
      unawaited(() async {
        for (var i = 0; i < 6; i++) {
          await sender.send(chunk);
          done++;
        }
      }());
      await pumpEventQueue();
      // 起始窗口 4 段，没有确认就停在这里
      expect(done, 4);
      expect(frames, hasLength(4));

      // 电脑的确认帧经连接送来，放出下一段，窗口加一段
      socket.incoming.add(
        SocketEvent.binary(
          desk.seal(
            bulkFrame(
              opUploadAck,
              5,
              Uint8List(8)
                ..buffer.asByteData().setUint64(0, bulkChunk, Endian.little),
            ),
            direction: dirDesktopToMobile,
            kind: kindBinary,
          ),
        ),
      );
      await pumpEventQueue();
      expect(sender.window, 5 * bulkChunk);
      expect(done, 6);

      // 往返比最小值多出排队上限：窗口缩小
      final before = sender.window;
      sender.ack(
        2 * bulkChunk,
        DateTime.now().add(const Duration(milliseconds: 500)),
      );
      expect(sender.window, before * 3 ~/ 4);

      // 旧电脑不回确认：不等
      final legacy = UploadSender(rpc, 6, acked: false);
      for (var i = 0; i < 8; i++) {
        await legacy.send(chunk);
      }
      sender.close();

      // 等确认时连接断了，马上报 closed，不等超时
      final stuck = UploadSender(rpc, 7, acked: true);
      for (var i = 0; i < 4; i++) {
        await stuck.send(chunk);
      }
      final waiting = expectLater(
        stuck.send(chunk),
        throwsA(isA<RpcException>().having((e) => e.code, 'code', 'closed')),
      );
      await pumpEventQueue();
      await rpc.close();
      await waiting;
      stuck.close();
    },
  );
}
