import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'protocol/link.dart';
import 'protocol/pairing.dart';
import 'protocol/rpc.dart';
import 'protocol/socket.dart';
import 'workspace/models.dart';

abstract class PairingStore {
  Future<String?> read();
  Future<void> write(String? code);
}

class MemoryPairingStore implements PairingStore {
  String? code;
  @override
  Future<String?> read() async => code;
  @override
  Future<void> write(String? value) async => code = value;
}

class PrefsPairingStore implements PairingStore {
  static const _key = 'hive.pairCode';

  @override
  Future<String?> read() async =>
      (await SharedPreferences.getInstance()).getString(_key);

  @override
  Future<void> write(String? code) async {
    final prefs = await SharedPreferences.getInstance();
    if (code == null) {
      await prefs.remove(_key);
    } else {
      await prefs.setString(_key, code);
    }
  }
}

class HiveAppModel extends ChangeNotifier {
  HiveAppModel({
    PairingStore? store,
    SocketConnector? connect,
    this.networkSignatures,
  }) : store = store ?? PrefsPairingStore(),
       connect = connect ?? WebSocketByteSocket.connect;

  final PairingStore store;
  final SocketConnector connect;
  final Stream<String>? networkSignatures;

  PairingOffer? offer;
  HiveLink? link;
  List<WorktreeRow> worktrees = const [];
  var connecting = false;
  String? error;
  Timer? _poll;
  var _connectGen = 0;

  PhoneRpc? get rpc => link?.rpc;

  Future<void> restore() async {
    final saved = await store.read();
    if (saved == null || saved.isEmpty) return;
    await connectCode(saved);
  }

  Future<void> connectCode(String raw) async {
    final gen = ++_connectGen;
    connecting = true;
    error = null;
    notifyListeners();
    HiveLink? next;
    try {
      final parsed = PairingOffer.parse(raw);
      final previous = link;
      link = null;
      await previous?.close();
      if (gen != _connectGen) return;
      final created = HiveLink(
        offer: parsed,
        connect: connect,
        networkSignatures: networkSignatures ?? connectivitySignatures(),
      );
      next = created;
      created.onActivity = () {
        if (gen != _connectGen || !identical(link, created)) return;
        if (created.rpc == null && created.lastError != null) {
          error = '${created.lastError}';
        }
        notifyListeners();
      };
      await created.start();
      if (gen != _connectGen) {
        await created.close();
        return;
      }
      link = next;
      offer = parsed;
      await store.write(raw.trim());
      // 先离开配对页。工作区列表回来再补，不让扫码结果卡在「正在连接」。
      notifyListeners();
      await refreshWorktrees();
      _poll?.cancel();
      _poll = Timer.periodic(const Duration(seconds: 3), (_) {
        unawaited(refreshWorktrees());
      });
    } catch (caught) {
      if (!identical(link, next)) await next?.close();
      if (gen == _connectGen) error = '$caught';
    } finally {
      if (gen == _connectGen) {
        connecting = false;
        notifyListeners();
      }
    }
  }

  Future<void> refreshWorktrees() async {
    final current = rpc;
    if (current == null) return;
    try {
      final result = await current.call('worktree.ps', {'limit': 10000});
      worktrees = parseWorktrees(result['worktrees']);
      error = null;
    } catch (caught) {
      error = '$caught';
    }
    notifyListeners();
  }

  Future<void> forget() async {
    _connectGen++;
    _poll?.cancel();
    final current = link;
    link = null;
    offer = null;
    worktrees = const [];
    await current?.close();
    await store.write(null);
    error = null;
    notifyListeners();
  }

  @override
  void dispose() {
    _connectGen++;
    _poll?.cancel();
    unawaited(link?.close());
    super.dispose();
  }
}
