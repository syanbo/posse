import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'protocol/link.dart';
import 'protocol/pairing.dart';
import 'protocol/rpc.dart';
import 'protocol/socket.dart';
import 'workspace/models.dart';

/// 把连接、协议层的报错换成看得懂的话；认不出的原样给出，方便排查。
String friendlyError(Object error) {
  if (error is TimeoutException) return '电脑没有回应，可能已经休眠或者网络断了';
  if (error is RpcException) return error.message;
  final text = '$error';
  if (error is StateError && text.contains('closed') ||
      text.contains('socket closed')) {
    return '和电脑的连接断开了，正在重连';
  }
  if (text.contains('SocketException') ||
      text.contains('Connection refused') ||
      text.contains('WebSocketChannelException')) {
    return '连不上电脑：确认电脑上的 Hive 开着，手机和电脑在同一个网络';
  }
  if (error is FormatException) return '配对码不对，重新扫一次或者重新复制';
  return text;
}

/// 保存配对过的电脑。内容是 [SavedComputers] 的 JSON；旧版只存一个配对码，读的时候兼容。
abstract class PairingStore {
  Future<String?> read();
  Future<void> write(String? data);
}

class MemoryPairingStore implements PairingStore {
  String? code;
  @override
  Future<String?> read() async => code;
  @override
  Future<void> write(String? value) async => code = value;
}

class PrefsPairingStore implements PairingStore {
  static const _key = 'hive.computers';
  static const _legacyKey = 'hive.pairCode';

  @override
  Future<String?> read() async {
    final prefs = await SharedPreferences.getInstance();
    return prefs.getString(_key) ?? prefs.getString(_legacyKey);
  }

  @override
  Future<void> write(String? data) async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.remove(_legacyKey);
    if (data == null) {
      await prefs.remove(_key);
    } else {
      await prefs.setString(_key, data);
    }
  }
}

/// 手机上的偏好。存在 SharedPreferences 的 hive.settings 里，和配对分开。
class AppSettings {
  AppSettings({
    this.theme = 'dark',
    this.openMode = 'chat',
    this.terminalFontSize = 12.5,
    this.showReasoning = true,
    this.expandTools = false,
  });

  /// dark、light 或 system。默认深色。
  String theme;

  /// 点会话先看什么：chat 对话，terminal 终端。没有原生存档的会话总是开终端。
  String openMode;
  double terminalFontSize;
  bool showReasoning;
  bool expandTools;

  Map<String, Object?> toJson() => {
    'theme': theme,
    'openMode': openMode,
    'terminalFontSize': terminalFontSize,
    'showReasoning': showReasoning,
    'expandTools': expandTools,
  };

  static AppSettings decode(String? data) {
    final out = AppSettings();
    if (data == null || data.isEmpty) return out;
    try {
      final map = jsonDecode(data);
      if (map is! Map) return out;
      final theme = map['theme'];
      if (theme is String && {'dark', 'light', 'system'}.contains(theme)) {
        out.theme = theme;
      }
      final open = map['openMode'];
      if (open is String && {'chat', 'terminal'}.contains(open)) {
        out.openMode = open;
      }
      final size = map['terminalFontSize'];
      if (size is num) out.terminalFontSize = size.toDouble().clamp(10, 18);
      if (map['showReasoning'] is bool) {
        out.showReasoning = map['showReasoning'] as bool;
      }
      if (map['expandTools'] is bool) {
        out.expandTools = map['expandTools'] as bool;
      }
    } catch (_) {}
    return out;
  }
}

abstract class SettingsStore {
  Future<String?> read();
  Future<void> write(String data);
}

class MemorySettingsStore implements SettingsStore {
  String? data;
  @override
  Future<String?> read() async => data;
  @override
  Future<void> write(String value) async => data = value;
}

class PrefsSettingsStore implements SettingsStore {
  static const _key = 'hive.settings';

  @override
  Future<String?> read() async =>
      (await SharedPreferences.getInstance()).getString(_key);

  @override
  Future<void> write(String data) async =>
      (await SharedPreferences.getInstance()).setString(_key, data);
}

/// 一台配对过的电脑。按主机公钥认：同一台电脑重新配对会替换掉旧的配对码。
class SavedComputer {
  SavedComputer({
    required this.id,
    required this.code,
    this.name = '',
    this.customName = '',
    List<String>? projectOrder,
    List<String>? collapsed,
  }) : projectOrder = projectOrder ?? [],
       collapsed = collapsed ?? [];

  final String id;
  String code;

  /// 电脑报上来的名字（status.get 的 machineName），没有时用地址。
  String name;

  /// 用户在手机上起的名字，优先于电脑报上来的。只存在手机上，不改电脑的主机名。
  String customName;

  /// 首页项目的顺序（项目名），在手机上拖动排好的。没列到的项目排在后面。
  List<String> projectOrder;

  /// 首页收起的项目（项目名）。
  List<String> collapsed;

  String get label {
    if (customName.isNotEmpty) return customName;
    if (name.isNotEmpty) return name;
    try {
      return Uri.parse(PairingOffer.parse(code).endpoint).host;
    } catch (_) {
      return '电脑';
    }
  }

  Map<String, Object?> toJson() => {
    'id': id,
    'code': code,
    'name': name,
    'customName': customName,
    'projectOrder': projectOrder,
    'collapsed': collapsed,
  };

  static SavedComputer? fromJson(Object? raw) {
    if (raw is! Map) return null;
    final id = raw['id'], code = raw['code'];
    if (id is! String || code is! String) return null;
    final order = raw['projectOrder'];
    final collapsed = raw['collapsed'];
    return SavedComputer(
      id: id,
      code: code,
      name: raw['name'] as String? ?? '',
      customName: raw['customName'] as String? ?? '',
      projectOrder: order is List ? order.whereType<String>().toList() : null,
      collapsed: collapsed is List
          ? collapsed.whereType<String>().toList()
          : null,
    );
  }
}

/// 存储里的全部内容：电脑列表和当前连的是哪台。
class SavedComputers {
  SavedComputers({List<SavedComputer>? computers, this.active})
    : computers = computers ?? [];

  final List<SavedComputer> computers;
  String? active;

  SavedComputer? get current {
    for (final c in computers) {
      if (c.id == active) return c;
    }
    return null;
  }

  String encode() => jsonEncode({
    'active': active,
    'computers': [for (final c in computers) c.toJson()],
  });

  static SavedComputers decode(String? data) {
    final text = data?.trim() ?? '';
    if (text.isEmpty) return SavedComputers();
    if (!text.startsWith('{')) {
      // 旧版存的是单个配对码
      try {
        final offer = PairingOffer.parse(text);
        return SavedComputers(
          computers: [SavedComputer(id: offer.publicKeyB64, code: text)],
          active: offer.publicKeyB64,
        );
      } catch (_) {
        return SavedComputers();
      }
    }
    try {
      final map = jsonDecode(text);
      if (map is! Map) return SavedComputers();
      final list = map['computers'];
      return SavedComputers(
        computers: [
          if (list is List)
            for (final item in list) ?SavedComputer.fromJson(item),
        ],
        active: map['active'] as String?,
      );
    } catch (_) {
      return SavedComputers();
    }
  }
}

class HiveAppModel extends ChangeNotifier {
  HiveAppModel({
    PairingStore? store,
    SettingsStore? settingsStore,
    SocketConnector? connect,
    this.networkSignatures,
  }) : store = store ?? PrefsPairingStore(),
       settingsStore = settingsStore ?? PrefsSettingsStore(),
       connect = connect ?? WebSocketByteSocket.connect;

  final PairingStore store;
  final SettingsStore settingsStore;
  var settings = AppSettings();
  final SocketConnector connect;
  final Stream<String>? networkSignatures;

  PairingOffer? offer;
  HiveLink? link;
  List<WorktreeRow> worktrees = const [];

  /// 有运行中终端的工作区的标签，首页照桌面侧栏把会话直接列在工作区下面。
  Map<String, List<SessionTab>> tabs = const {};
  var connecting = false;
  String? error;
  Timer? _poll;
  var _connectGen = 0;
  var saved = SavedComputers();

  PhoneRpc? get rpc => link?.rpc;

  /// 现在连着的地址，没连上时为空。
  String? get connectedUrl => link?.rpc == null ? null : link?.url;

  /// 配对过的电脑，同一时间只连其中一台。
  List<SavedComputer> get computers => saved.computers;

  SavedComputer? get current => saved.current;

  /// 改一项设置：先改内存里的再存盘，界面马上跟着变。
  void updateSettings(void Function(AppSettings s) change) {
    change(settings);
    unawaited(settingsStore.write(jsonEncode(settings.toJson())));
    notifyListeners();
  }

  Future<void> restore() async {
    settings = AppSettings.decode(await settingsStore.read());
    saved = SavedComputers.decode(await store.read());
    notifyListeners();
    final active = saved.current ?? saved.computers.firstOrNull;
    if (active == null) return;
    await connectCode(active.code);
  }

  /// 连一台电脑：新配对码或已保存的电脑都走这里。连上之后才断开原来那台，连不上时原来的连接不受影响。
  /// 返回这次有没有连上；连不上的原因在 [error]。
  Future<bool> connectCode(String raw) async {
    final gen = ++_connectGen;
    connecting = true;
    error = null;
    notifyListeners();
    HiveLink? next;
    try {
      final parsed = PairingOffer.parse(raw);
      final created = HiveLink(
        offer: parsed,
        connect: connect,
        networkSignatures: networkSignatures ?? connectivitySignatures(),
      );
      next = created;
      created.onActivity = () {
        if (gen != _connectGen || !identical(link, created)) return;
        if (created.rpc == null && created.lastError != null) {
          error = friendlyError(created.lastError!);
        }
        notifyListeners();
      };
      await created.start();
      if (gen != _connectGen) {
        await created.close();
        return false;
      }
      final previous = link;
      final switched = offer?.publicKeyB64 != parsed.publicKeyB64;
      link = next;
      offer = parsed;
      if (switched) {
        worktrees = const [];
        tabs = const {};
      }
      await previous?.close();
      _remember(parsed, raw.trim());
      // 先离开配对页。工作区列表回来再补，不让扫码结果卡在「正在连接」。
      notifyListeners();
      unawaited(_fetchName(created, parsed.publicKeyB64));
      await refreshWorktrees();
      _poll?.cancel();
      _poll = Timer.periodic(const Duration(seconds: 3), (_) {
        unawaited(refreshWorktrees());
      });
      return true;
    } catch (caught) {
      if (!identical(link, next)) await next?.close();
      if (gen == _connectGen) error = friendlyError(caught);
      return false;
    } finally {
      if (gen == _connectGen) {
        connecting = false;
        notifyListeners();
      }
    }
  }

  void _remember(PairingOffer offer, String code) {
    final id = offer.publicKeyB64;
    final existing = saved.computers.where((c) => c.id == id).firstOrNull;
    if (existing == null) {
      saved.computers.add(SavedComputer(id: id, code: code));
    } else {
      existing.code = code;
    }
    saved.active = id;
    unawaited(store.write(saved.encode()));
  }

  /// 电脑名取自 status.get，桌面端报的是 "Hive 主机名"。
  Future<void> _fetchName(HiveLink link, String id) async {
    try {
      final rpc = link.rpc;
      if (rpc == null) return;
      final status = await rpc.call('status.get', const {});
      var name = status['machineName'] as String? ?? '';
      name = name.replaceFirst(RegExp(r'^Hive\s+'), '').trim();
      final computer = saved.computers.where((c) => c.id == id).firstOrNull;
      if (computer == null || name.isEmpty || computer.name == name) return;
      computer.name = name;
      await store.write(saved.encode());
      notifyListeners();
    } catch (_) {}
  }

  /// 连不上时重试当前这台；还没选过就连第一台。
  Future<void> retry() async {
    final computer = saved.current ?? saved.computers.firstOrNull;
    if (computer == null) return;
    if (rpc != null) return refreshWorktrees();
    await connectCode(computer.code);
  }

  /// 在手机上给电脑改名；传空字符串退回电脑报上来的名字。
  void renameComputer(String id, String name) {
    final computer = saved.computers.where((c) => c.id == id).firstOrNull;
    if (computer == null) return;
    computer.customName = name.trim();
    unawaited(store.write(saved.encode()));
    notifyListeners();
  }

  Future<void> switchTo(String id) async {
    final computer = saved.computers.where((c) => c.id == id).firstOrNull;
    if (computer == null || computer.id == offer?.publicKeyB64) return;
    await connectCode(computer.code);
  }

  /// 忘掉一台电脑。忘掉的是正在连的那台时，接着连剩下的第一台，一台都没有就回到配对页。
  Future<void> removeComputer(String id) async {
    saved.computers.removeWhere((c) => c.id == id);
    final wasActive = offer?.publicKeyB64 == id || saved.active == id;
    if (wasActive) saved.active = null;
    await store.write(saved.computers.isEmpty ? null : saved.encode());
    if (!wasActive) {
      notifyListeners();
      return;
    }
    await _disconnect();
    final next = saved.computers.firstOrNull;
    if (next != null) await connectCode(next.code);
  }

  /// 忘掉当前这台电脑。
  Future<void> forget() async {
    final id = offer?.publicKeyB64 ?? saved.active;
    if (id == null) {
      await _disconnect();
      return;
    }
    await removeComputer(id);
  }

  Future<void> _disconnect() async {
    _connectGen++;
    _poll?.cancel();
    final current = link;
    link = null;
    offer = null;
    worktrees = const [];
    tabs = const {};
    error = null;
    connecting = false;
    notifyListeners();
    await current?.close();
  }

  /// 按手机上排好的顺序排项目名；新出现的项目保持电脑给的顺序，排在后面。
  List<String> orderProjects(List<String> names) {
    final order = current?.projectOrder ?? const <String>[];
    final ranked = [
      for (final name in order)
        if (names.contains(name)) name,
    ];
    return [
      ...ranked,
      for (final name in names)
        if (!ranked.contains(name)) name,
    ];
  }

  bool isCollapsed(String project) =>
      current?.collapsed.contains(project) ?? false;

  /// 展开或收起首页的一个项目，按电脑分别记住。
  void toggleProject(String project) {
    final computer = current;
    if (computer == null) return;
    if (!computer.collapsed.remove(project)) computer.collapsed.add(project);
    unawaited(store.write(saved.encode()));
    notifyListeners();
  }

  void setProjectOrder(List<String> names) {
    final computer = current;
    if (computer == null) return;
    computer.projectOrder = List.of(names);
    unawaited(store.write(saved.encode()));
    notifyListeners();
  }

  Future<void> refreshWorktrees() async {
    final current = rpc;
    if (current == null) return;
    try {
      final result = await current.call('worktree.ps', {'limit': 10000});
      final rows = parseWorktrees(result['worktrees']);
      final live = rows.where((r) => r.live > 0).toList();
      final lists = await Future.wait([
        for (final row in live)
          current
              .call('session.tabs.list', {'worktree': row.id})
              .then((res) => parseTabs(row.id, res['tabs']))
              // 单个工作区读失败时沿用上一次的，不让整页报错
              .catchError((Object _) => tabs[row.id] ?? const <SessionTab>[]),
      ]);
      if (!identical(current, rpc)) return;
      worktrees = rows;
      tabs = {for (var i = 0; i < live.length; i++) live[i].id: lists[i]};
      error = null;
    } catch (caught) {
      error = friendlyError(caught);
    }
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
