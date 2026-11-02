import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:flutter/widgets.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'protocol/e2ee.dart';
import 'protocol/link.dart';
import 'protocol/pairing.dart';
import 'protocol/quic_socket.dart';
import 'protocol/race.dart';
import 'protocol/rpc.dart';
import 'settings/custom_keys.dart';
import 'settings/phone_model.dart';
import 'terminal/frames.dart';
import 'workspace/models.dart';

/// 把连接、协议层的报错换成看得懂的话；认不出的原样给出，方便排查。
String friendlyError(Object error) {
  if (error is TimeoutException) return '电脑没有回应，可能已经休眠或者网络断了';
  if (error is FirstConnectFailed) {
    return '连不上电脑：确认电脑上的 Posse 开着、手机和电脑在同一个网络，'
        '并在手机设置里允许 Posse 使用无线数据和本地网络';
  }
  if (error is RpcException) {
    // 新版电脑端已经给中文说明；旧版只给英文，常见的错误码在这里认
    return switch (error.code) {
      'closed' => '和电脑的连接断开了，正在重连',
      'method_not_found' ||
      'unknown_method' => '电脑上的 Posse 是旧版，不支持这个功能：在电脑上更新 Posse',
      'binary_file' => '这是二进制文件，没法当文本打开',
      'file_too_large' => '文件太大，手机上打不开',
      'not_a_file' => '这不是文件',
      'invalid_relative_path' => '路径不在工作区里',
      _ => error.message,
    };
  }
  final text = '$error';
  if (error is StateError && text.contains('closed') ||
      text.contains('socket closed')) {
    return '和电脑的连接断开了，正在重连';
  }
  if (text.contains('SocketException') ||
      text.contains('Connection refused') ||
      text.contains('WebSocketChannelException')) {
    return '连不上电脑：确认电脑上的 Posse 开着，手机和电脑在同一个网络';
  }
  if (error is FormatException) return '配对码不对，重新扫一次或者重新复制';
  return text;
}

/// 第一次配对一直连不上：可能电脑没开，也可能没给网络权限，见 [PosseAppModel._start]。
class FirstConnectFailed implements Exception {
  const FirstConnectFailed();
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
  static const _key = 'posse.computers';
  static const _legacyKey = 'posse.pairCode';

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

/// 看桌面设置的版本。1 起看桌面不再是实验，默认打开、用触控板。
const deskVersion = 1;

/// 手机上的偏好。存在 SharedPreferences 的 posse.settings 里，和配对分开。
class AppSettings {
  AppSettings({
    this.theme = 'dark',
    this.language = '',
    this.openMode = 'terminal',
    this.terminalFontSize = 12.5,
    this.showReasoning = true,
    this.expandTools = false,
    this.deskLayout = false,
    this.deskZoom = 1,
    this.deskWatch = true,
    this.deskLandscape = false,
    this.deskGesturesSeen = false,
    this.deskTrackpad = true,
    this.historyByFolder = false,
    this.projectMetaHidden = false,
    this.workspaceMetaHidden = false,
    this.homeByStatus = false,
    this.skippedUpdateBuild = 0,
    this.phoneId = '',
    Map<String, int>? deskDisplays,
    List<BarKey>? barKeys,
  }) : barKeys = barKeys ?? defaultBarKeys(),
       deskDisplays = deskDisplays ?? {};

  /// dark、light 或 system。默认深色。
  String theme;

  /// 界面语言：空串跟随系统，zh 或 en。
  String language;

  /// 点会话先看什么：chat 对话，terminal 终端。没有原生存档的会话总是开终端。
  String openMode;
  double terminalFontSize;
  bool showReasoning;
  bool expandTools;

  /// 终端按电脑的布局看：不改终端尺寸，把电脑的格子缩进屏幕宽度。默认按手机屏幕重排。
  bool deskLayout;

  /// 电脑布局在「刚好缩进屏幕宽度」之上的倍数，吸在 0.5–2 的固定档。1 是一整行落在屏幕里。
  double deskZoom;

  /// 电脑菜单里显示「看桌面」，默认开。只在有桌面插件的 iOS、安卓上露出开关。
  bool deskWatch;

  /// 看桌面时锁横屏。离开页面恢复原来的方向，下次进来按这个锁。
  bool deskLandscape;

  /// 看桌面的手势提示已经关过。只出现一次。
  bool deskGesturesSeen;

  /// 看桌面把屏幕当触控板：单指滑动移指针，点按落在指针上。默认开，关了是直接点哪按哪。
  bool deskTrackpad;

  /// 历史页的会话按文件夹分组，而不是按时间。
  bool historyByFolder;

  /// 首页的项目行不显示分支和路径，只留名字。和桌面设置里的同名开关一样。
  bool projectMetaHidden;

  /// 首页项目下面的工作区行不显示分支，只留名字。
  bool workspaceMetaHidden;

  /// 首页是「运行中」：所有项目的 agent 会话平铺成一列，而不是按项目分。
  bool homeByStatus;

  /// 首页更新提示点了 × 的构建号：这个构建不再提示，更新的构建照常提示。
  int skippedUpdateBuild;

  /// 每台电脑上次在看桌面里选的屏，键是电脑的公钥，值是屏的 ID（CGDirectDisplayID，同一台显示器重启后不变）。
  /// 下次看时订阅带上，那块屏不在了电脑看主屏。
  Map<String, int> deskDisplays;

  /// 这台手机的安装 ID，第一次启动时生成。电脑靠它把同一台手机扫过的几个码认成一台。
  String phoneId;

  /// 终端快捷键栏上的键和顺序（键盘开关除外，它固定在最左边），内置的和自己加的混排。
  List<BarKey> barKeys;

  List<CustomKey> get customKeys => [for (final key in barKeys) ?key.custom];

  Map<String, Object?> toJson() => {
    'theme': theme,
    'language': language,
    'openMode': openMode,
    'terminalFontSize': terminalFontSize,
    'showReasoning': showReasoning,
    'expandTools': expandTools,
    'deskLayout': deskLayout,
    'deskZoom': deskZoom,
    'deskWatch': deskWatch,
    'deskLandscape': deskLandscape,
    'deskGesturesSeen': deskGesturesSeen,
    'deskTrackpad': deskTrackpad,
    'deskVersion': deskVersion,
    'historyByFolder': historyByFolder,
    'projectMetaHidden': projectMetaHidden,
    'workspaceMetaHidden': workspaceMetaHidden,
    'homeByStatus': homeByStatus,
    'skippedUpdateBuild': skippedUpdateBuild,
    'deskDisplays': deskDisplays,
    'phoneId': phoneId,
    'barKeys': [for (final k in barKeys) k.toJson()],
    'barKeysVersion': barKeysVersion,
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
      final language = map['language'];
      if (language is String && {'', 'zh', 'en'}.contains(language)) {
        out.language = language;
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
      if (map['deskLayout'] is bool) {
        out.deskLayout = map['deskLayout'] as bool;
      }
      final zoom = map['deskZoom'];
      if (zoom is num) out.deskZoom = zoom.toDouble().clamp(0.5, 2);
      // 看桌面还是实验时存下的开关是旧默认值（关、直接点按），不算用户选的：换成新的默认一次
      final deskChosen = map['deskVersion'] == deskVersion;
      if (deskChosen && map['deskWatch'] is bool) {
        out.deskWatch = map['deskWatch'] as bool;
      }
      if (map['deskLandscape'] is bool) {
        out.deskLandscape = map['deskLandscape'] as bool;
      }
      if (map['deskGesturesSeen'] is bool) {
        out.deskGesturesSeen = map['deskGesturesSeen'] as bool;
      }
      if (deskChosen && map['deskTrackpad'] is bool) {
        out.deskTrackpad = map['deskTrackpad'] as bool;
      } else if (map['deskTrackpad'] == false) {
        // 从直接点按换成了触控板，下次进看桌面再提示一次手势
        out.deskGesturesSeen = false;
      }
      if (map['historyByFolder'] is bool) {
        out.historyByFolder = map['historyByFolder'] as bool;
      }
      if (map['projectMetaHidden'] is bool) {
        out.projectMetaHidden = map['projectMetaHidden'] as bool;
      }
      if (map['workspaceMetaHidden'] is bool) {
        out.workspaceMetaHidden = map['workspaceMetaHidden'] as bool;
      }
      if (map['homeByStatus'] is bool) {
        out.homeByStatus = map['homeByStatus'] as bool;
      }
      if (map['skippedUpdateBuild'] is int) {
        out.skippedUpdateBuild = map['skippedUpdateBuild'] as int;
      }
      final displays = map['deskDisplays'];
      if (displays is Map) {
        out.deskDisplays = {
          for (final MapEntry(:key, :value) in displays.entries)
            if (key is String && value is int) key: value,
        };
      }
      final phoneId = map['phoneId'];
      if (phoneId is String) out.phoneId = phoneId;
      final bar = map['barKeys'];
      final custom = map['customKeys'];
      if (bar is List) {
        // 版本 2 之前存的立即发送是读旧设置时补在最后的，不是自己挪的：丢掉让它回到 Enter 后面
        final placed = map['barKeysVersion'] == barKeysVersion;
        out.barKeys = normalizeBarKeys([
          for (final k in bar)
            if (BarKey.fromJson(k) case final key?)
              if (placed || key.builtin != sendNowLabel) key,
        ]);
      } else if (custom is List) {
        out.barKeys = defaultBarKeys([
          for (final k in custom) ?CustomKey.fromJson(k),
        ]);
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
  static const _key = 'posse.settings';

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
    List<String>? collapsedWorkspaces,
    List<String>? quic,
  }) : projectOrder = projectOrder ?? [],
       collapsed = collapsed ?? [],
       collapsedWorkspaces = collapsedWorkspaces ?? [],
       quic = quic ?? [];

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

  /// 首页收起的工作区（工作区 ID），项目有多个工作区时才用得上。
  List<String> collapsedWorkspaces;

  /// 电脑上次给的 QUIC 地址（[PosseLink.quicUrls]，经中转的带着这台手机的令牌）。冷启动直接拨，
  /// 在外面不用先经隧道连上 WebSocket。
  List<String> quic;

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
    'collapsedWorkspaces': collapsedWorkspaces,
    'quic': quic,
  };

  static SavedComputer? fromJson(Object? raw) {
    if (raw is! Map) return null;
    final id = raw['id'], code = raw['code'];
    if (id is! String || code is! String) return null;
    final order = raw['projectOrder'];
    final collapsed = raw['collapsed'];
    final collapsedWorkspaces = raw['collapsedWorkspaces'];
    return SavedComputer(
      id: _migrateId(id, code),
      code: code,
      name: raw['name'] as String? ?? '',
      customName: raw['customName'] as String? ?? '',
      projectOrder: order is List ? order.whereType<String>().toList() : null,
      collapsed: collapsed is List
          ? collapsed.whereType<String>().toList()
          : null,
      collapsedWorkspaces: collapsedWorkspaces is List
          ? collapsedWorkspaces.whereType<String>().toList()
          : null,
      quic: raw['quic'] is List
          ? (raw['quic'] as List).whereType<String>().toList()
          : null,
    );
  }
}

/// 手机上一台"电脑"的 ID：电脑的公钥加配对码里的主地址。同一台电脑扫远程（隧道）和局域网的码
/// 是两项，各走各的地址，可以来回切；只按公钥认的话，后扫的会把先扫的挤掉。
String computerId(PairingOffer offer) =>
    '${offer.publicKeyB64}@${offer.endpoint.trim()}';

/// 旧版只按公钥存 ID，读出来时按它的配对码补上地址。
String _migrateId(String id, String code) {
  if (id.contains('@')) return id;
  try {
    return computerId(PairingOffer.parse(code));
  } catch (_) {
    return id;
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
          computers: [SavedComputer(id: computerId(offer), code: text)],
          active: computerId(offer),
        );
      } catch (_) {
        return SavedComputers();
      }
    }
    try {
      final map = jsonDecode(text);
      if (map is! Map) return SavedComputers();
      final list = map['computers'];
      final computers = [
        if (list is List)
          for (final item in list) ?SavedComputer.fromJson(item),
      ];
      var active = map['active'] as String?;
      if (active != null && !active.contains('@')) {
        active = computers
            .where((c) => c.id.startsWith('$active@'))
            .firstOrNull
            ?.id;
      }
      return SavedComputers(computers: computers, active: active);
    } catch (_) {
      return SavedComputers();
    }
  }
}

class PosseAppModel extends ChangeNotifier {
  PosseAppModel({
    PairingStore? store,
    SettingsStore? settingsStore,
    SocketConnector? connect,
    QuicPuncher? punch,
    this.networkSignatures,
  }) : store = store ?? PrefsPairingStore(),
       settingsStore = settingsStore ?? PrefsSettingsStore(),
       connect = connect ?? connectAny,
       // 换了连接器（测试）时默认不打洞：打洞要真的 UDP
       punch = punch ?? (connect == null ? punchQuic : null);

  final PairingStore store;
  final SettingsStore settingsStore;
  var settings = AppSettings();
  final SocketConnector connect;
  final QuicPuncher? punch;
  final Stream<String>? networkSignatures;

  /// 第一次配对时连不上，每隔 [firstConnectRetry] 重试，App 在前台累计 [firstConnectWindow] 为止，见 [_start]。
  Duration firstConnectWindow = const Duration(seconds: 30);
  Duration firstConnectRetry = const Duration(seconds: 1);

  /// 系统权限弹窗开着时 App 不在前台（inactive）。测试里替换。
  bool Function() inForeground = () =>
      WidgetsBinding.instance.lifecycleState != AppLifecycleState.inactive &&
      WidgetsBinding.instance.lifecycleState != AppLifecycleState.paused;

  PairingOffer? offer;
  PosseLink? link;
  List<WorktreeRow> worktrees = const [];

  /// 项目图标的图片，键是文件名：桌面每次导入都换一个名字，取过的不会过期。正在取的值是 null。
  final Map<String, Uint8List?> _projectIcons = {};

  /// 取失败的图标和失败的时间：首页每 3 秒重建一次，隔一会儿再取，不每次重建都去要。
  final Map<String, DateTime> _iconFailed = {};

  /// 项目图标的图片；还没取过就向电脑要，取到后刷新界面。
  Uint8List? projectIcon(String name) {
    if (_projectIcons.containsKey(name)) return _projectIcons[name];
    final failed = _iconFailed[name];
    if (failed != null &&
        DateTime.now().difference(failed) < const Duration(seconds: 30)) {
      return null;
    }
    final current = rpc;
    if (current == null) return null;
    _projectIcons[name] = null;
    current
        .call('posse.projectIcon', {'name': name})
        .then((res) {
          final data = res['data'];
          if (data is! String) throw const FormatException('no data');
          _projectIcons[name] = base64Decode(data);
          _iconFailed.remove(name);
          notifyListeners();
        })
        .catchError((Object _) {
          _projectIcons.remove(name);
          _iconFailed[name] = DateTime.now();
        });
    return null;
  }

  /// 有运行中终端的工作区的标签，首页照桌面侧栏把会话直接列在工作区下面。
  Map<String, List<SessionTab>> tabs = const {};

  /// 首页「运行中」里会话的顺序（终端 ID），null 表示下次按状态重排。只在内存里，见 [updateRunOrder]。
  List<String>? runOrder;

  /// 断过线、重连后还没拉到新的会话列表：[tabs] 里的状态是断线前的，「运行中」先不按它排。
  bool tabsStale = false;
  var connecting = false;

  /// 用户在电脑列表里点了断开：停在首页、不自动重连，点重新连接或选一台电脑才再连。只在这次运行里有效。
  var disconnected = false;
  String? error;
  Timer? _poll;
  var _connectGen = 0;
  var saved = SavedComputers();

  PhoneRpc? get rpc => link?.rpc;

  /// 连接停在失败上：没在连，也没有连接在后台重连（[PosseLink] 只在连上过之后断线时一直重连，
  /// 一开始就没连上时没有它），等用户点重试。
  bool get failed => !connecting && !disconnected && link == null;

  /// 和电脑之间的往返延迟，没连上或还没测到时为空。
  Duration? get latency => link?.rpc == null ? null : link?.meter?.latency;

  /// 最近一次测延迟超时，期间也没收到电脑的任何数据。
  bool get unresponsive =>
      link?.rpc != null && (link?.meter?.unresponsive ?? false);

  /// 现在连着的地址，没连上时为空。
  String? get connectedUrl => link?.rpc == null ? null : link?.url;

  /// 配对过的电脑，同一时间只连其中一台。
  List<SavedComputer> get computers => saved.computers;

  SavedComputer? get current => saved.current;

  /// 列表里显示的名字：同一台电脑存了几个地址（远程、局域网）时，没改过名的后面带上地址区分。
  String labelOf(SavedComputer? c) {
    if (c == null) return '';
    if (c.customName.isNotEmpty) return c.label;
    final key = c.id.split('@').first;
    final twins = saved.computers.where((o) => o.id.split('@').first == key);
    if (twins.length < 2) return c.label;
    try {
      final uri = Uri.parse(PairingOffer.parse(c.code).endpoint);
      return '${c.label}（${uri.host}）';
    } catch (_) {
      return c.label;
    }
  }

  /// 正在连（或连着）的那一项的 ID，见 [computerId]。
  String? get activeId {
    final o = offer;
    return o == null ? null : computerId(o);
  }

  /// 改一项设置：先改内存里的再存盘，界面马上跟着变。
  void updateSettings(void Function(AppSettings s) change) {
    change(settings);
    unawaited(settingsStore.write(jsonEncode(settings.toJson())));
    notifyListeners();
  }

  /// 界面语言变了（设置里换了，或跟随的系统语言变了）：再报一次给连着的电脑，之后的报错和说明按新语言给。
  void localeChanged() {
    final o = offer;
    if (o == null) return;
    unawaited(
      link?.rpc
          ?.declareCapabilities(phone: _phoneInfo(o))
          .catchError((Object _) {}),
    );
  }

  /// 本地的设置和电脑列表读完了，闪屏等它再退。
  bool restored = false;

  Future<void> restore() async {
    settings = AppSettings.decode(await settingsStore.read());
    if (settings.phoneId.isEmpty) {
      settings.phoneId = hexOf(randomBytes(16));
      unawaited(settingsStore.write(jsonEncode(settings.toJson())));
    }
    _phoneName ??= await phoneModel();
    saved = SavedComputers.decode(await store.read());
    restored = true;
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
    disconnected = false;
    error = null;
    notifyListeners();
    PosseLink? next;
    try {
      final parsed = PairingOffer.parse(raw);
      final created = PosseLink(
        offer: parsed,
        connect: connect,
        networkSignatures: networkSignatures ?? connectivitySignatures(),
        phone: () => _phoneInfo(parsed),
      );
      next = created;
      final id = computerId(parsed);
      created.quicUrls = List.of(
        saved.computers.where((c) => c.id == id).firstOrNull?.quic ?? const [],
      );
      created.punch = punch;
      created.onQuicUrls = (urls) {
        final computer = saved.computers.where((c) => c.id == id).firstOrNull;
        if (computer == null || listEquals(computer.quic, urls)) return;
        computer.quic = List.of(urls);
        unawaited(store.write(saved.encode()));
      };
      PhoneRpc? seen;
      created.onActivity = () {
        if (gen != _connectGen || !identical(link, created)) return;
        final now = created.rpc;
        if (now == null) {
          // 断线期间按项目的先后排，连上后拉到新的状态再按状态排
          if (seen != null) {
            runOrder = null;
            tabsStale = true;
          }
          if (created.lastError != null) {
            error = friendlyError(created.lastError!);
          }
        } else if (!identical(now, seen)) {
          // 换上了新连接（重连、升级到 QUIC）：断线期间的会话状态可能已经变了，马上拉一次，
          // 不等下一轮轮询。重试失败留下的报错也不再作数，不然首页会先闪一下红色的「连不上电脑」
          error = null;
          unawaited(refreshWorktrees());
        }
        seen = now;
        notifyListeners();
      };
      created.onLatency = () {
        if (gen == _connectGen && identical(link, created)) notifyListeners();
      };
      await _start(created, gen, first: saved.computers.isEmpty);
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
        runOrder = null;
      }
      await previous?.close();
      _remember(parsed, raw.trim());
      // 新扫的码顶掉了同一项里的旧码：再报一次，电脑那边马上作废旧码
      unawaited(
        created.rpc
            ?.declareCapabilities(phone: _phoneInfo(parsed))
            .catchError((Object _) {}),
      );
      // 先离开配对页。工作区列表回来再补，不让扫码结果卡在「正在连接」。
      notifyListeners();
      unawaited(_fetchName(created, computerId(parsed)));
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

  /// 从后台回到前台，见 [PosseLink.wake]。
  Future<void> wake() async {
    if (disconnected) return;
    await link?.wake();
    // 在后台时轮询停着，列表停在离开时的样子：回来马上拉一次。连接死了的话重连上之后会拉
    await refreshWorktrees();
  }

  /// iOS 在 App 第一次连局域网地址时才弹「本地网络」权限，弹窗开着时连接直接失败。
  /// 所以第一次配对连不上时接着重试，等用户点完允许自然连上。
  /// 弹窗开着时 App 不在前台，这段时间不算进时限，用户看多久都行；点允许前后网络状态不变，
  /// 等不到网络变化的通知。配对过的电脑权限早就点过了，只连一次。
  Future<void> _start(PosseLink link, int gen, {required bool first}) async {
    if (!first) {
      await link.start();
      return;
    }
    var left = firstConnectWindow;
    while (true) {
      final started = DateTime.now();
      try {
        await link.start();
        return;
      } catch (error) {
        // 电脑回了话（密钥不对、拒绝能力声明），等多久都一样
        if (error is E2eeException ||
            error is RpcException ||
            error is RaceCancelled) {
          rethrow;
        }
        if (gen != _connectGen) rethrow;
      }
      await Future<void>.delayed(firstConnectRetry);
      if (gen != _connectGen) throw const RaceCancelled();
      if (inForeground()) left -= DateTime.now().difference(started);
      if (left <= Duration.zero) throw const FirstConnectFailed();
    }
  }

  String? _phoneName;

  /// 报给电脑的手机身份：安装 ID、型号，和存着的这台电脑（同一个公钥）的全部 token。
  Map<String, Object?>? _phoneInfo(PairingOffer offer) {
    if (settings.phoneId.isEmpty) return null;
    final tokens = <String>{offer.deviceToken};
    for (final c in saved.computers) {
      try {
        final o = PairingOffer.parse(c.code);
        if (o.publicKeyB64 == offer.publicKeyB64) tokens.add(o.deviceToken);
      } catch (_) {}
    }
    return {
      'id': settings.phoneId,
      'name': _phoneName ?? '',
      'tokens': tokens.toList(),
    };
  }

  void _remember(PairingOffer offer, String code) {
    final id = computerId(offer);
    final existing = saved.computers.where((c) => c.id == id).firstOrNull;
    if (existing == null) {
      saved.computers.add(SavedComputer(id: id, code: code));
    } else {
      existing.code = code;
    }
    saved.active = id;
    unawaited(store.write(saved.encode()));
  }

  /// 电脑名取自 status.get，桌面端报的是 "Posse 主机名"。
  Future<void> _fetchName(PosseLink link, String id) async {
    try {
      final rpc = link.rpc;
      if (rpc == null) return;
      final status = await rpc.call('status.get', const {});
      var name = status['machineName'] as String? ?? '';
      name = name.replaceFirst(RegExp(r'^Posse\s+'), '').trim();
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
    if (computer == null || computer.id == activeId) return;
    await connectCode(computer.code);
  }

  /// 忘掉一台电脑。忘掉的是正在连的那台时，接着连剩下的第一台，一台都没有就回到配对页。
  Future<void> removeComputer(String id) async {
    saved.computers.removeWhere((c) => c.id == id);
    final wasActive = activeId == id || saved.active == id;
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
    final id = activeId ?? saved.active;
    if (id == null) {
      await _disconnect();
      return;
    }
    await removeComputer(id);
  }

  /// 主动断开当前这台电脑，配对留着。
  Future<void> disconnect() async {
    await _disconnect();
    disconnected = true;
    notifyListeners();
  }

  Future<void> _disconnect() async {
    _connectGen++;
    _poll?.cancel();
    final current = link;
    link = null;
    offer = null;
    worktrees = const [];
    tabs = const {};
    runOrder = null;
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

  bool isWorkspaceCollapsed(String id) =>
      current?.collapsedWorkspaces.contains(id) ?? false;

  /// 展开或收起首页的一个工作区，按电脑分别记住。
  void toggleWorkspace(String id) {
    final computer = current;
    if (computer == null) return;
    if (!computer.collapsedWorkspaces.remove(id)) {
      computer.collapsedWorkspaces.add(id);
    }
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
      // 全是还没恢复的终端时也要拉，首页才列得出它们
      final live = rows.where((r) => r.terminals > 0).toList();
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
      tabsStale = false;
      error = null;
    } catch (caught) {
      // 换了连接（升级到 QUIC、重连）后旧连接上的失败不算：新连接马上会再拉一次
      if (!identical(current, rpc)) return;
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
