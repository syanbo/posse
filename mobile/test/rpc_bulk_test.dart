import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
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
  for (final framing in [framingBinary, framingText]) {
    test(
      'blob chunks fill the reply fields, terminal frames still stream (framing $framing)',
      () async {
        final desk = E2eeSession(
          recvKey: _key(1),
          sendKey: _key(2),
          id: Uint8List(32),
          transcriptHash: Uint8List(32),
        )..framing = framing;
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
          socket.incoming.add(
            binary || desk.binaryMessages
                ? SocketEvent.binary(frame)
                : SocketEvent.text(utf8.encode(base64.encode(frame))),
          );
        }

        final terminal = rpc.binary.first;
        final reply = rpc.call('git.diff', const {});
        await sent.moveNext();
        expect(sent.current.binary, desk.binaryMessages);
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
        push(
          bulkFrame(opBlobChunk, 7, content.sublist(bulkChunk)),
          binary: true,
        );
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
  }
}
