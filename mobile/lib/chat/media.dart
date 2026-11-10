import 'dart:async';
import 'dart:io';
import 'dart:typed_data';

import 'package:flutter/material.dart';

import '../app_state.dart';
import '../files/save.dart';
import '../files/video.dart';
import '../history/models.dart' show historyTime;
import '../l10n/l10n.dart';
import '../protocol/rpc.dart';
import '../ui/loading.dart';
import '../ui/media_pager.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../workspace/models.dart';

/// 能列图片和视频的 agent，同桌面图片面板。
const mediaAgents = {'claude', 'codex', 'grok', 'cursor'};

bool hasMedia(SessionTab tab) =>
    mediaAgents.contains(tab.agent) && (tab.sessionId?.isNotEmpty ?? false);

/// 一项的名字：没有名字的一类电脑只发代号（`tag`），按界面语言写出叫法。
String mediaLabel(AppLocalizations l, String? tag, String label) =>
    switch (tag) {
      'generated' => l.chatMediaGenerated,
      'codeOutput' => l.chatMediaCodeOutput,
      'pasted' => l.chatMediaPasted,
      _ => label,
    };

/// 会话里的一张图片或一个视频（`session.media` 的一项）。
class MediaItem {
  const MediaItem({
    required this.id,
    required this.video,
    required this.label,
    required this.source,
    this.tag,
    this.at,
    this.size,
  });

  factory MediaItem.fromJson(Map<String, Object?> j) {
    final at = (j['at'] as num?)?.toInt() ?? 0;
    return MediaItem(
      id: j['id'] as String? ?? '',
      video: j['kind'] == 'video',
      label: j['label'] as String? ?? '',
      tag: j['tag'] as String?,
      source: j['source'] as String? ?? '',
      at: at == 0 ? null : DateTime.fromMillisecondsSinceEpoch(at),
      size: (j['size'] as num?)?.toInt(),
    );
  }

  final String id;
  final bool video;

  /// 电脑给的名字（文件名、工具名或 #序号），存到手机时当文件名；没有名字的一类为空，看 [tag]。
  final String label;

  /// 没有名字时是哪一类（generated、codeOutput、pasted），显示用 [name]。
  final String? tag;

  /// 显示的名字：有名字用名字，否则按界面语言写出这一类的叫法。
  String name(AppLocalizations l) => mediaLabel(l, tag, label);

  /// 读的文件或返回它的工具。
  final String source;
  final DateTime? at;

  /// 磁盘上的文件才知道大小；会话文件里内嵌的图片读第一段时才知道。
  final int? size;
}

class MediaList {
  const MediaList({
    this.made = const [],
    this.madeAll = 0,
    this.pasted = const [],
  });

  factory MediaList.fromJson(Map<String, Object?> j) {
    List<MediaItem> items(Object? v) => [
      for (final e in (v as List?) ?? const [])
        if (e is Map) MediaItem.fromJson(e.cast<String, Object?>()),
    ];
    final made = items(j['made']);
    return MediaList(
      made: made,
      madeAll: (j['madeAll'] as num?)?.toInt() ?? made.length,
      pasted: items(j['pasted']),
    );
  }

  final List<MediaItem> made;
  final int madeAll;
  final List<MediaItem> pasted;

  bool get isEmpty => made.isEmpty && pasted.isEmpty;
}

Map<String, Object?> _sessionArgs(SessionTab tab) => {
  'agent': tab.agent,
  'sessionId': tab.sessionId,
  if (tab.transcriptPath != null) 'transcriptPath': tab.transcriptPath,
};

/// 缩略图按会话和 ID 记在内存里，来回进出这一页不重新要；一张约 30 KB，最多留 [_thumbMax] 张。
final _thumbs = <String, Uint8List>{};
const _thumbMax = 120;

/// 会话的"图片与视频"：agent 生成的在上（截图、生成的图片、录屏），粘贴的在下，都是新的在前。
/// 开着时每 3 秒重列一次（同桌面面板），新截的图自己出现。点开看大图或播放。
class MediaScreen extends StatefulWidget {
  const MediaScreen({super.key, required this.model, required this.tab});

  final PosseAppModel model;
  final SessionTab tab;

  @override
  State<MediaScreen> createState() => _MediaScreenState();
}

class _MediaScreenState extends State<MediaScreen> {
  MediaList? _list;
  String? _error;
  Timer? _timer;
  var _busy = false;

  @override
  void initState() {
    super.initState();
    _refresh();
    _timer = Timer.periodic(const Duration(seconds: 3), (_) {
      if (!appAway) _refresh();
    });
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }

  Future<void> _refresh() async {
    final rpc = widget.model.rpc;
    if (_busy || rpc == null) return;
    _busy = true;
    try {
      final res = await rpc.call('session.media', _sessionArgs(widget.tab));
      if (mounted && _list == null) await routeSettled(context);
      if (mounted) {
        setState(() {
          _list = MediaList.fromJson(res);
          _error = null;
        });
      }
    } on RpcException catch (e) {
      if (e.code == 'method_not_found') {
        _timer?.cancel();
        if (mounted) {
          setState(() => _error = context.l10n.chatMediaOldPosse);
        }
      } else if (mounted && _list == null) {
        setState(() => _error = friendlyError(e, context.l10n));
      }
    } catch (e) {
      // 列过一次之后断线不打扰，下一轮再列
      if (mounted && _list == null) {
        setState(() => _error = friendlyError(e, context.l10n));
      }
    } finally {
      _busy = false;
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(titleSpacing: 0, title: Text(context.l10n.chatMediaTitle)),
      body: SideSafe(child: _body(context)),
    );
  }

  Widget _body(BuildContext context) {
    final c = PosseColors.of(context);
    final list = _list;
    if (list == null) {
      if (_error != null) {
        return EmptyState(
          icon: Icons.error_outline_rounded,
          text: _error!,
          error: true,
        );
      }
      return const Center(child: PosseLoader());
    }
    if (list.isEmpty) {
      return EmptyState(
        icon: Icons.photo_library_rounded,
        text: context.l10n.chatMediaNone,
      );
    }
    final now = DateTime.now();
    // 点开后按这个顺序左右翻：先生成的、再粘贴的，和网格一致
    final all = [...list.made, ...list.pasted];
    return CustomScrollView(
      slivers: [
        if (list.made.isNotEmpty) ...[
          _header(c, context.l10n.chatMediaMadeHeader, list.madeAll),
          _grid(all, 0, list.made.length, now),
          if (list.madeAll > list.made.length)
            SliverToBoxAdapter(
              child: Padding(
                padding: const EdgeInsets.fromLTRB(16, 10, 16, 0),
                child: Text(
                  context.l10n.chatMediaRecentOnly(list.made.length),
                  style: TextStyle(fontSize: Fs.small, color: c.muted),
                ),
              ),
            ),
        ],
        if (list.pasted.isNotEmpty) ...[
          _header(c, context.l10n.chatMediaPastedHeader, list.pasted.length),
          _grid(all, list.made.length, list.pasted.length, now),
        ],
        const SliverSafeArea(
          top: false,
          sliver: SliverToBoxAdapter(child: SizedBox(height: 24)),
        ),
      ],
    );
  }

  Widget _header(PosseColors c, String title, int n) => SliverToBoxAdapter(
    child: Padding(
      padding: const EdgeInsets.fromLTRB(16, 20, 16, 10),
      child: Row(
        children: [
          Text(
            title,
            style: TextStyle(
              fontSize: Fs.small,
              fontWeight: Fw.bold,
              color: c.text,
            ),
          ),
          const SizedBox(width: 6),
          Text(
            '$n',
            style: TextStyle(
              fontSize: Fs.small,
              color: c.muted,
              fontFeatures: const [FontFeature.tabularFigures()],
            ),
          ),
        ],
      ),
    ),
  );

  /// [all] 里从 [start] 起的 [count] 项。
  Widget _grid(List<MediaItem> all, int start, int count, DateTime now) =>
      SliverPadding(
        padding: const EdgeInsets.symmetric(horizontal: 12),
        sliver: SliverGrid.builder(
          gridDelegate: const SliverGridDelegateWithFixedCrossAxisCount(
            crossAxisCount: 2,
            mainAxisSpacing: 12,
            crossAxisSpacing: 10,
            childAspectRatio: 0.72,
          ),
          itemCount: count,
          itemBuilder: (context, i) => _MediaTile(
            key: ValueKey(all[start + i].id),
            model: widget.model,
            tab: widget.tab,
            all: all,
            index: start + i,
            now: now,
          ),
        ),
      );
}

/// 存到哪：图片进相册，相册不收的视频（webm）安卓存下载、iOS 分享。
SaveTo _target(MediaItem item) => saveTarget(item.label, image: !item.video);

/// 一格：缩略图（竖的截图从顶上裁），视频盖播放标记，下面是名字和时间。点开看，长按存到手机。
class _MediaTile extends StatefulWidget {
  const _MediaTile({
    super.key,
    required this.model,
    required this.tab,
    required this.all,
    required this.index,
    required this.now,
  });

  final PosseAppModel model;
  final SessionTab tab;

  /// 这一页的全部项和这一格在其中的位置，点开后从这里左右翻。
  final List<MediaItem> all;
  final int index;
  final DateTime now;

  MediaItem get item => all[index];

  @override
  State<_MediaTile> createState() => _MediaTileState();
}

class _MediaTileState extends State<_MediaTile> {
  Uint8List? _thumb;
  var _failed = false;

  /// 上一次去取缩略图用的连接和失败的时间：换了连接（重连）马上再取，同一条连接隔一阵再试。
  PhoneRpc? _tried;
  DateTime? _failedAt;
  var _loading = false;

  static const _retryAfter = Duration(seconds: 30);

  String get _key => '${widget.tab.sessionId}\n${widget.item.id}';

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_onModel);
    _thumb = _thumbs.remove(_key);
    if (_thumb != null) {
      _thumbs[_key] = _thumb!;
    } else {
      _load();
    }
  }

  @override
  void dispose() {
    widget.model.removeListener(_onModel);
    super.dispose();
  }

  void _onModel() {
    if (_thumb != null || _loading) return;
    final rpc = widget.model.rpc;
    if (rpc == null) return;
    final at = _failedAt;
    if (!identical(rpc, _tried) ||
        at != null && DateTime.now().difference(at) >= _retryAfter) {
      _load();
    }
  }

  Future<void> _load() async {
    final rpc = widget.model.rpc;
    if (rpc == null) return;
    _tried = rpc;
    _loading = true;
    try {
      final res = await rpc.call('session.mediaThumb', {
        ..._sessionArgs(widget.tab),
        'id': widget.item.id,
      }, timeout: const Duration(seconds: 40));
      final bytes = rpcBytes(res['content']);
      _thumbs[_key] = bytes;
      while (_thumbs.length > _thumbMax) {
        _thumbs.remove(_thumbs.keys.first);
      }
      if (mounted) setState(() => _thumb = bytes);
    } catch (_) {
      _failedAt = DateTime.now();
      if (mounted) setState(() => _failed = true);
    } finally {
      _loading = false;
    }
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final item = widget.item;
    final thumb = _thumb;
    final time = historyTime(
      context.l10n,
      item.at,
      widget.now,
      h24: use24Hour(context),
    );
    return Semantics(
      button: true,
      label: item.video
          ? context.l10n.chatMediaVideoLabel(item.name(context.l10n))
          : context.l10n.chatMediaImageLabel(item.name(context.l10n)),
      child: GestureDetector(
        behavior: HitTestBehavior.opaque,
        onTap: () => Navigator.of(context).push(
          MaterialPageRoute<void>(
            builder: (_) => MediaViewer(
              model: widget.model,
              tab: widget.tab,
              items: widget.all,
              index: widget.index,
            ),
          ),
        ),
        onLongPress: () {
          final rpc = widget.model.rpc;
          if (rpc == null) return;
          showSaveSheet(
            context,
            to: _target(item),
            onSave: (to) => downloadAndSave(
              context,
              name: item.label,
              to: to,
              fetch: (cancelled, onProgress) => fetchMedia(
                rpc,
                widget.tab,
                item,
                cancelled: cancelled,
                onProgress: onProgress,
              ),
            ),
          );
        },
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Expanded(
              child: ClipRRect(
                borderRadius: BorderRadius.circular(Rd.input),
                child: Container(
                  color: c.fill,
                  width: double.infinity,
                  child: Stack(
                    fit: StackFit.expand,
                    children: [
                      if (thumb != null)
                        Image.memory(
                          thumb,
                          fit: BoxFit.cover,
                          alignment: Alignment.topCenter,
                          gaplessPlayback: true,
                        )
                      else if (_failed)
                        Icon(
                          item.video
                              ? Icons.videocam_off_rounded
                              : Icons.broken_image_rounded,
                          color: c.faint,
                        ),
                      if (item.video) const Center(child: _PlayMark()),
                    ],
                  ),
                ),
              ),
            ),
            const SizedBox(height: 6),
            Row(
              children: [
                Expanded(
                  child: Text(
                    item.name(context.l10n),
                    overflow: TextOverflow.ellipsis,
                    maxLines: 1,
                    style: TextStyle(fontSize: Fs.small, color: c.muted),
                  ),
                ),
                if (time.isNotEmpty) ...[
                  const SizedBox(width: 6),
                  Text(
                    time,
                    style: TextStyle(
                      fontSize: Fs.small,
                      color: c.faint,
                      fontFeatures: const [FontFeature.tabularFigures()],
                    ),
                  ),
                ],
              ],
            ),
          ],
        ),
      ),
    );
  }
}

class _PlayMark extends StatelessWidget {
  const _PlayMark();

  @override
  Widget build(BuildContext context) {
    return Container(
      width: 40,
      height: 40,
      decoration: const BoxDecoration(
        color: PosseColors.scrim,
        shape: BoxShape.circle,
      ),
      child: const Icon(
        Icons.play_arrow_rounded,
        size: Ic.large,
        color: Colors.white,
      ),
    );
  }
}

/// 把会话里的一项分段取到手机上（`session.mediaRead`），同一项只取一次。
Future<File> fetchMedia(
  PhoneRpc rpc,
  SessionTab tab,
  MediaItem item, {
  required bool Function() cancelled,
  required void Function(int done, int total) onProgress,
}) async {
  final size = item.size;
  if (item.video && size != null && size > videoMax) {
    throw const VideoTooLarge();
  }
  final dot = item.label.lastIndexOf('.');
  final ext = item.video && dot >= 0 ? item.label.substring(dot) : '';
  return fetchChunked(
    key: 'media\n${tab.agent}\n${tab.sessionId}\n${item.id}',
    ext: ext,
    size: size,
    read: (offset, limit, timeout) => rpc.call('session.mediaRead', {
      ..._sessionArgs(tab),
      'id': item.id,
      'offset': offset,
      'limit': limit,
    }, timeout: timeout),
    cancelled: cancelled,
    onProgress: onProgress,
  );
}

/// 看一项：图片取原图（agent 当时看到的样子），双指放大；视频下好后播放。取到手机上后顶栏可以存到手机。
/// 从 [items] 的第 [index] 项打开，左右滑切到前一项、后一项（[MediaPager]），顶栏跟着换。
class MediaViewer extends StatefulWidget {
  const MediaViewer({
    super.key,
    required this.model,
    required this.tab,
    required this.items,
    required this.index,
  });

  final PosseAppModel model;
  final SessionTab tab;
  final List<MediaItem> items;
  final int index;

  @override
  State<MediaViewer> createState() => _MediaViewerState();
}

class _MediaViewerState extends State<MediaViewer> {
  late var _index = widget.index;

  /// 画面用的连接。重连后换成新的，「重试」不再用断掉的那条。
  late PhoneRpc? _rpc = widget.model.rpc;

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_onModel);
  }

  @override
  void dispose() {
    widget.model.removeListener(_onModel);
    super.dispose();
  }

  void _onModel() {
    if (!identical(widget.model.rpc, _rpc) && mounted) {
      setState(() => _rpc = widget.model.rpc);
    }
  }

  /// 已经取到手机上的项，按位置记：翻回来时顶栏的存到手机还能用。
  final _files = <int, File>{};

  void _ready(int i, File file) {
    if (mounted) setState(() => _files[i] = file);
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final items = widget.items;
    final item = items[_index];
    final tab = widget.tab;
    final rpc = _rpc;
    final file = _files[_index];
    final to = _target(item);
    final time = historyTime(
      context.l10n,
      item.at,
      DateTime.now(),
      h24: use24Hour(context),
    );
    final sub = [
      if (item.source != item.name(context.l10n)) item.source,
      if (time.isNotEmpty) time,
      if (items.length > 1) '${_index + 1}/${items.length}',
    ].join(' · ');
    return Scaffold(
      appBar: AppBar(
        titleSpacing: 0,
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              item.name(context.l10n),
              overflow: TextOverflow.ellipsis,
              style: TextStyle(
                fontSize: Fs.title,
                fontWeight: Fw.bold,
                color: c.text,
              ),
            ),
            if (sub.isNotEmpty)
              Text(
                sub,
                overflow: TextOverflow.ellipsis,
                style: statusLineStyle(c.muted),
              ),
          ],
        ),
        actions: [
          Builder(
            builder: (button) => IconButton(
              tooltip: saveLabel(context.l10n, to),
              icon: Icon(saveIcon(to)),
              onPressed: file == null
                  ? null
                  : () => saveToPhone(button, file, item.label),
            ),
          ),
          const SizedBox(width: 4),
        ],
      ),
      body: SideSafe(
        child: rpc == null
            ? EmptyState(
                icon: Icons.link_off_rounded,
                text: context.l10n.errorNotConnected,
                error: true,
              )
            : MediaPager(
                count: items.length,
                initialIndex: widget.index,
                onChanged: (i) => setState(() => _index = i),
                builder: (context, i) {
                  final item = items[i];
                  if (item.video) {
                    return VideoView(
                      key: ValueKey(item.id),
                      onReady: (file) => _ready(i, file),
                      fetch: (cancelled, onProgress) => fetchMedia(
                        rpc,
                        tab,
                        item,
                        cancelled: cancelled,
                        onProgress: onProgress,
                      ),
                    );
                  }
                  return _ImageView(
                    key: ValueKey(item.id),
                    rpc: rpc,
                    tab: tab,
                    item: item,
                    onReady: (file) => _ready(i, file),
                  );
                },
              ),
      ),
    );
  }
}

class _ImageView extends StatefulWidget {
  const _ImageView({
    super.key,
    required this.rpc,
    required this.tab,
    required this.item,
    required this.onReady,
  });

  final PhoneRpc rpc;
  final SessionTab tab;
  final MediaItem item;
  final ValueChanged<File> onReady;

  @override
  State<_ImageView> createState() => _ImageViewState();
}

class _ImageViewState extends State<_ImageView> {
  File? _file;
  String? _error;
  var _disposed = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    _disposed = true;
    super.dispose();
  }

  Future<void> _load() async {
    try {
      final file = await fetchMedia(
        widget.rpc,
        widget.tab,
        widget.item,
        cancelled: () => _disposed,
        onProgress: (_, _) {},
      );
      if (!mounted) return;
      setState(() => _file = file);
      widget.onReady(file);
    } on VideoCancelled {
      return;
    } catch (e) {
      if (mounted) setState(() => _error = friendlyError(e, context.l10n));
    }
  }

  @override
  Widget build(BuildContext context) {
    if (_error != null) {
      return EmptyState(
        icon: Icons.broken_image_rounded,
        text: _error!,
        error: true,
        action: TextButton(
          onPressed: () {
            setState(() => _error = null);
            _load();
          },
          child: Text(context.l10n.commonRetry),
        ),
      );
    }
    final file = _file;
    if (file == null) return const Center(child: PosseLoader());
    return ZoomImage(child: Image.file(file));
  }
}
