import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/app_state.dart';

String pairCode(String key, String endpoint) {
  final json = jsonEncode({
    'deviceToken': 't',
    'endpoint': endpoint,
    'publicKeyB64': key,
    'v': 2,
  });
  return 'orca://pair?code=${base64Url.encode(utf8.encode(json))}';
}

void main() {
  test('the old single pairing code becomes the active computer', () {
    final code = pairCode('k1', 'ws://192.168.1.2:6788');
    final saved = SavedComputers.decode(code);
    expect(saved.computers, hasLength(1));
    expect(saved.active, 'k1@ws://192.168.1.2:6788');
    expect(saved.current!.label, '192.168.1.2');
  });

  test('computers round trip with names and project order', () {
    final saved = SavedComputers(
      computers: [
        SavedComputer(
          id: 'k1',
          code: pairCode('k1', 'ws://a:1'),
          name: 'MacBook',
          projectOrder: ['hive', 'Works'],
        ),
        SavedComputer(id: 'k2', code: pairCode('k2', 'ws://b:2')),
      ],
      active: 'k2',
    );
    final back = SavedComputers.decode(saved.encode());
    expect(back.computers.map((c) => c.label), ['MacBook', 'b']);
    // 旧版只按公钥存的 ID 读出来补上地址，当前选中的跟着换
    expect(back.current!.id, 'k2@ws://b:2');
    expect(back.computers.first.projectOrder, ['hive', 'Works']);
    expect(SavedComputers.decode('not json').computers, isEmpty);
  });

  test('projects follow the phone order and new ones go last', () {
    final model = HiveAppModel(store: MemoryPairingStore());
    model.saved = SavedComputers(
      computers: [
        SavedComputer(id: 'k', code: '', projectOrder: ['b', 'gone', 'a']),
      ],
      active: 'k',
    );
    expect(model.orderProjects(['a', 'b', 'c']), ['b', 'a', 'c']);
    model.setProjectOrder(['c', 'a', 'b']);
    expect(model.orderProjects(['a', 'b', 'c', 'd']), ['c', 'a', 'b', 'd']);
  });

  test('a name given on the phone wins and clearing it falls back', () {
    final model = HiveAppModel(store: MemoryPairingStore());
    final computer = SavedComputer(
      id: 'k',
      code: pairCode('k', 'ws://10.0.0.2:6788'),
      name: 'MacBook-Pro-4',
    );
    model.saved = SavedComputers(computers: [computer], active: 'k');
    model.renameComputer('k', '  公司电脑 ');
    expect(computer.label, '公司电脑');
    final back = SavedComputers.decode(model.saved.encode());
    expect(back.computers.single.label, '公司电脑');
    model.renameComputer('k', '');
    expect(computer.label, 'MacBook-Pro-4');
  });

  test('the same computer can be saved once per address', () async {
    final store = MemoryPairingStore();
    final model = HiveAppModel(store: store);
    final remote = pairCode('k', 'wss://tun.example/hive');
    final lan = pairCode('k', 'ws://192.168.1.5:6788');
    model.saved = SavedComputers(
      computers: [SavedComputer(id: 'k', code: remote, name: 'MacBook')],
    );
    // 旧版的 ID 迁移后，再存一项同一台电脑的局域网地址，两项都在
    model.saved = SavedComputers.decode(model.saved.encode());
    model.saved.computers.add(
      SavedComputer(id: 'k@ws://192.168.1.5:6788', code: lan, name: 'MacBook'),
    );
    expect(model.saved.computers.map((c) => c.id), [
      'k@wss://tun.example/hive',
      'k@ws://192.168.1.5:6788',
    ]);
    expect(model.computers.map(model.labelOf), [
      'MacBook（tun.example）',
      'MacBook（192.168.1.5）',
    ]);
    model.renameComputer('k@ws://192.168.1.5:6788', '家里');
    expect(model.labelOf(model.computers.last), '家里');
  });
}
