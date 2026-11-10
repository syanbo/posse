import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';

import '../app_state.dart';
import '../chat/image_pick.dart';
import '../l10n/l10n.dart';
import '../protocol/rpc.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'api.dart';
import 'models.dart';

/// 头像按内容的 sha 留在内存里：同一个 sha 的图不会变，取到一次就一直用（每张 256 的 JPEG，几十 KB）。
/// 取不到的（旧电脑、还没同步到、断线）不留，下次再试。
abstract final class DiscAvatars {
  static const keep = 64;
  static final _bytes = <String, Uint8List>{};
  static final _loading = <String, Future<Uint8List?>>{};

  static Uint8List? peek(String sha) {
    final hit = _bytes.remove(sha);
    if (hit != null) _bytes[sha] = hit;
    return hit;
  }

  static Future<Uint8List?> get(DiscCall call, String sha) {
    final hit = peek(sha);
    if (hit != null) return Future.value(hit);
    final pending = _loading[sha];
    if (pending != null) return pending;
    final next = _load(call, sha);
    _loading[sha] = next;
    next.whenComplete(() {
      if (identical(_loading[sha], next)) _loading.remove(sha);
    });
    return next;
  }

  static Future<Uint8List?> _load(DiscCall call, String sha) async {
    try {
      final r = await call('profile.avatar', {'sha': sha});
      final bytes = rpcBytes(r['data']);
      if (bytes.isEmpty) return null;
      _bytes[sha] = bytes;
      while (_bytes.length > keep) {
        _bytes.remove(_bytes.keys.first);
      }
      return bytes;
    } catch (_) {
      return null;
    }
  }

  @visibleForTesting
  static void clear() {
    _bytes.clear();
    _loading.clear();
  }
}

/// 人的圆形头像：有头像画图，没有（或取不到）画名字的第一个字。
class PersonAvatar extends StatefulWidget {
  const PersonAvatar({
    super.key,
    required this.call,
    required this.sha,
    required this.name,
    required this.size,
    this.bytes,
  });

  /// 取头像用的讨论接口；为 null 时只画首字。
  final DiscCall? call;
  final String sha, name;
  final double size;

  /// 还没保存的预览图，优先于 [sha]。
  final Uint8List? bytes;

  @override
  State<PersonAvatar> createState() => _PersonAvatarState();
}

class _PersonAvatarState extends State<PersonAvatar> {
  Uint8List? data;

  @override
  void initState() {
    super.initState();
    fetch();
  }

  @override
  void didUpdateWidget(PersonAvatar old) {
    super.didUpdateWidget(old);
    if (old.sha != widget.sha) fetch();
  }

  void fetch() {
    final sha = widget.sha, call = widget.call;
    data = sha.isEmpty ? null : DiscAvatars.peek(sha);
    if (sha.isEmpty || call == null || data != null) return;
    unawaited(
      DiscAvatars.get(call, sha).then((b) {
        if (mounted && widget.sha == sha) setState(() => data = b);
      }),
    );
  }

  @override
  Widget build(BuildContext context) {
    final initial = DiscInitial(name: widget.name, size: widget.size);
    final bytes = widget.bytes ?? data;
    if (bytes == null) return initial;
    return ClipOval(
      child: Image.memory(
        bytes,
        width: widget.size,
        height: widget.size,
        fit: BoxFit.cover,
        gaplessPlayback: true,
        errorBuilder: (_, _, _) => initial,
      ),
    );
  }
}

/// 没有头像时的占位：淡底圆里写名字的第一个字。
class DiscInitial extends StatelessWidget {
  const DiscInitial({super.key, required this.name, required this.size});
  final String name;
  final double size;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final first = name.trim().isEmpty ? '?' : name.trim().characters.first;
    return Container(
      width: size,
      height: size,
      alignment: Alignment.center,
      decoration: BoxDecoration(color: c.tint, shape: BoxShape.circle),
      child: Text(
        first.toUpperCase(),
        style: TextStyle(
          // 画成图标的字按尺寸定字号：大头像（资料页）按边长的比例
          fontSize: size > Ic.large
              ? size * 0.4
              : size > _small
              ? Fs.body
              : Fs.tiny,
          fontWeight: Fw.medium,
          color: c.accent,
        ),
      ),
    );
  }
}

/// 消息里的小头像边长，大于它的首字用正文字号。
const double _small = 20;

/// 资料页的头像预览边长。
const double _preview = 96;

/// 打开名字和头像页；保存过返回 true。
Future<bool> openProfile(BuildContext context, DiscussionApi api) async {
  final saved = await Navigator.of(context)
      .push<bool>(MaterialPageRoute(builder: (_) => ProfileScreen(api: api)));
  return saved == true;
}

/// 名字和头像：属于当前连接的电脑的安装身份，它的所有讨论都用这一份。
class ProfileScreen extends StatefulWidget {
  const ProfileScreen({super.key, required this.api});
  final DiscussionApi api;
  @override
  State<ProfileScreen> createState() => _ProfileScreenState();
}

class _ProfileScreenState extends State<ProfileScreen> {
  final name = TextEditingController();
  DiscJson profile = {};
  Uint8List? picked;
  bool remove = false, loading = true, saving = false, invalid = false;
  Object? error;

  @override
  void initState() {
    super.initState();
    unawaited(load());
  }

  @override
  void dispose() {
    name.dispose();
    super.dispose();
  }

  Future<void> load() async {
    try {
      final r = await widget.api.call('profile.get', {});
      if (!mounted) return;
      setState(() {
        profile = r;
        name.text = discText(r, 'name');
        loading = false;
      });
    } catch (e) {
      if (mounted) {
        setState(() {
          error = e;
          loading = false;
        });
      }
    }
  }

  Future<void> choose() async {
    try {
      final bytes = await pickAvatarImage();
      if (bytes == null || !mounted) return;
      // 手机到电脑的一条消息最多 4 MB，图按 base64 发，超过 2.5 MB 就换一张
      if (bytes.length > 2500 * 1024) {
        setState(() => error = context.l10n.profileImageTooLarge);
        return;
      }
      setState(() {
        picked = bytes;
        remove = false;
        error = null;
      });
    } catch (e) {
      if (mounted) setState(() => error = e);
    }
  }

  Future<void> save() async {
    final text = name.text.trim();
    if (text.isEmpty || text.characters.length > 60) {
      setState(() => invalid = true);
      return;
    }
    setState(() {
      saving = true;
      invalid = false;
      error = null;
    });
    try {
      final r = await widget.api.call('profile.set', {
        if (text != discText(profile, 'name')) 'name': text,
        if (picked case final bytes?) 'data': base64Encode(bytes),
        if (picked == null && remove) 'remove': true,
      });
      if (!mounted) return;
      if (discList(r['pending']).isEmpty) {
        Navigator.of(context).pop(true);
        return;
      }
      // 还有讨论没收到，留在这页让人看到是哪些
      setState(() {
        profile = r;
        name.text = discText(r, 'name');
        picked = null;
        remove = false;
      });
    } catch (e) {
      if (mounted) setState(() => error = e);
    } finally {
      if (mounted) setState(() => saving = false);
    }
  }

  String reason(DiscJson p) {
    final l = context.l10n;
    final r = discText(p, 'reason');
    return switch (r) {
      'offline' => l.profileOffline,
      'unsupported' => l.profileUnsupported,
      // 其余是电脑给的英文原文，reasonText 是按手机语言译好的
      _ => discText(p, 'reasonText').isEmpty ? r : discText(p, 'reasonText'),
    };
  }

  @override
  Widget build(BuildContext context) {
    final l = context.l10n;
    final c = PosseColors.of(context);
    final model = widget.api.model;
    final shownError = error;
    final hasAvatar =
        picked != null || !remove && discText(profile, 'avatar').isNotEmpty;
    final pending = discList(profile['pending']);
    return Scaffold(
      appBar: AppBar(
        title: Text(l.profileTitle),
        actions: [
          TextButton(
            onPressed: loading || saving || profile.isEmpty ? null : save,
            child: Text(l.profileSave),
          ),
        ],
      ),
      body: SideSafe(
        child: loading
            ? const Center(child: HexSpinner(size: Ic.hero))
            : ListView(
                padding: const EdgeInsets.only(top: 8, bottom: 32),
                children: [
                  if (shownError != null)
                    NoticeBar(
                      shownError is String
                          ? shownError
                          : friendlyError(shownError, l),
                      onClose: () => setState(() => error = null),
                    ),
                  if (profile.isNotEmpty) ...[
                    Padding(
                      padding: const EdgeInsets.symmetric(vertical: 16),
                      child: Center(
                        child: ValueListenableBuilder(
                          valueListenable: name,
                          builder: (_, value, _) => PersonAvatar(
                            call: widget.api.call,
                            sha: remove ? '' : discText(profile, 'avatar'),
                            name: value.text,
                            size: _preview,
                            bytes: picked,
                          ),
                        ),
                      ),
                    ),
                    if (picked != null)
                      Padding(
                        padding: const EdgeInsets.only(bottom: 12),
                        child: Text(
                          l.profileCrop,
                          textAlign: TextAlign.center,
                          style: TextStyle(fontSize: Fs.small, color: c.muted),
                        ),
                      ),
                    Section(
                      children: [
                        RowTile(
                          leading: Icon(
                            Icons.photo_library_rounded,
                            size: Ic.medium,
                            color: c.accent,
                          ),
                          title: Text(l.profileChoose),
                          chevron: false,
                          onTap: saving ? null : choose,
                        ),
                        if (hasAvatar)
                          RowTile(
                            leading: Icon(
                              Icons.delete_outline_rounded,
                              size: Ic.medium,
                              color: c.danger,
                            ),
                            title: Text(
                              l.profileRemove,
                              style: TextStyle(color: c.danger),
                            ),
                            chevron: false,
                            onTap: saving
                                ? null
                                : () => setState(() {
                                    picked = null;
                                    remove = true;
                                  }),
                          ),
                      ],
                    ),
                    Padding(
                      padding: const EdgeInsets.fromLTRB(16, 0, 16, 20),
                      child: TextField(
                        controller: name,
                        enabled: !saving,
                        maxLength: 60,
                        textInputAction: TextInputAction.done,
                        onSubmitted: (_) => save(),
                        decoration: InputDecoration(
                          labelText: l.profileName,
                          errorText: invalid ? l.profileNameInvalid : null,
                        ),
                      ),
                    ),
                    Padding(
                      padding: const EdgeInsets.fromLTRB(20, 0, 20, 20),
                      child: Text(
                        l.profileNote(model.labelOf(model.current, l)),
                        style: TextStyle(
                          fontSize: Fs.small,
                          height: Lh.body,
                          color: c.muted,
                        ),
                      ),
                    ),
                    if (pending.isNotEmpty)
                      Section(
                        title: l.profilePendingTitle,
                        children: [
                          for (final p in pending)
                            RowTile(
                              chevron: false,
                              title: Text(
                                l.profilePending(
                                  discText(p, 'title').isEmpty
                                      ? discText(p, 'room')
                                      : discText(p, 'title'),
                                  reason(p),
                                ),
                              ),
                              subtitle: discText(p, 'host').isEmpty
                                  ? null
                                  : Text(discText(p, 'host')),
                            ),
                        ],
                      ),
                  ],
                ],
              ),
      ),
    );
  }
}

/// 设置页里的「名字和头像」：连着的电脑认得 profile.get 才显示，旧电脑不显示。
class ProfileSection extends StatefulWidget {
  const ProfileSection({super.key, required this.model});
  final PosseAppModel model;
  @override
  State<ProfileSection> createState() => _ProfileSectionState();
}

class _ProfileSectionState extends State<ProfileSection> {
  DiscussionApi? api;
  DiscJson profile = {};
  Object? rpc;
  int gen = 0;

  Future<void> load() async {
    final current = DiscussionApi(widget.model);
    final mine = ++gen;
    api = current;
    profile = {};
    try {
      final r = await current.call('profile.get', {});
      if (mounted && mine == gen) setState(() => profile = r);
    } catch (_) {
      // 旧电脑没有这个接口、断线：不显示这一节
      if (mounted && mine == gen) setState(() => profile = {});
    }
  }

  @override
  Widget build(BuildContext context) {
    final model = widget.model;
    // 换了电脑或重连了再取一次（设置页随 model 重建）
    if (!identical(model.rpc, rpc)) {
      rpc = model.rpc;
      if (rpc != null) {
        unawaited(load());
      } else {
        gen++;
        profile = {};
      }
    }
    final a = api;
    if (profile.isEmpty || a == null || !a.connected) {
      return const SizedBox.shrink();
    }
    final l = context.l10n;
    return Section(
      title: l.profileTitle,
      inset: 16 + Ic.large + 12,
      children: [
        RowTile(
          leading: PersonAvatar(
            call: a.call,
            sha: discText(profile, 'avatar'),
            name: discText(profile, 'name'),
            size: Ic.large,
          ),
          title: Text(discText(profile, 'name')),
          subtitle: Text(l.profileRowNote(model.labelOf(model.current, l))),
          onTap: () async {
            if (await openProfile(context, a) && mounted) await load();
          },
        ),
      ],
    );
  }
}
