import 'dart:async';

import 'package:flutter/material.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../app_state.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';

// 各 agent 的套餐用量（Hive 扩展 hive.usage.v1 的 usage.get）。电脑上的 Hive 只在 agent 运行时
// 顺带更新读数，手机这边定时取一次就够了。

DateTime? _ms(Object? v) {
  final n = (v as num?)?.toInt() ?? 0;
  return n > 0 ? DateTime.fromMillisecondsSinceEpoch(n) : null;
}

/// 一个限额窗口：用了多少、多长、什么时候重置。[name] 是分池（Claude 的 Fable、Cursor 的 Auto 和 API），
/// 空的是总额度；[at] 是分池自己的读数时间，和整家的不是同一份读数时才有。
class UsageWindow {
  const UsageWindow({
    required this.percent,
    this.minutes = 0,
    this.resetsAt,
    this.name = '',
    this.at,
  });

  final double percent;
  final int minutes;
  final DateTime? resetsAt;
  final String name;
  final DateTime? at;

  factory UsageWindow.fromJson(Map<String, Object?> j) => UsageWindow(
    percent: (j['percent'] as num?)?.toDouble() ?? 0,
    minutes: (j['minutes'] as num?)?.toInt() ?? 0,
    resetsAt: _ms(j['resetsAt']),
    name: j['name'] as String? ?? '',
    at: _ms(j['at']),
  );
}

/// 金额，单位美分；[limit] 为 0 是没有上限。
class UsageMoney {
  const UsageMoney({required this.used, this.limit = 0, this.at});

  final double used;
  final double limit;
  final DateTime? at;

  static UsageMoney? fromJson(Object? v) {
    if (v is! Map) return null;
    return UsageMoney(
      used: (v['used'] as num?)?.toDouble() ?? 0,
      limit: (v['limit'] as num?)?.toDouble() ?? 0,
      at: _ms(v['at']),
    );
  }
}

class AgentUsage {
  const AgentUsage({
    required this.agent,
    required this.windows,
    required this.updatedAt,
    this.plan = '',
    this.spend,
    this.onDemand,
    this.shares = const [],
    this.sharesAt,
    this.resets,
    this.resetsExpireAt,
    this.resetsAt,
  });

  final String agent;
  final String plan;
  final List<UsageWindow> windows;
  final DateTime updatedAt;

  /// 套餐内的金额（Cursor）
  final UsageMoney? spend;

  /// 超出套餐后按量付费的部分
  final UsageMoney? onDemand;

  /// 本周用量用在了哪（Claude）：名字和百分比
  final List<(String, double)> shares;
  final DateTime? sharesAt;

  /// 重置卡（Codex）：张数、最早一张的过期时间、读数时间
  final int? resets;
  final DateTime? resetsExpireAt;
  final DateTime? resetsAt;

  factory AgentUsage.fromJson(Map<String, Object?> j) {
    final shares = j['shares'] as Map?;
    final resets = j['resets'] as Map?;
    return AgentUsage(
      agent: j['agent'] as String? ?? '',
      plan: j['plan'] as String? ?? '',
      windows: [
        for (final w in (j['windows'] as List?) ?? const [])
          UsageWindow.fromJson((w as Map).cast<String, Object?>()),
      ],
      updatedAt: _ms(j['updatedAt']) ?? DateTime.fromMillisecondsSinceEpoch(0),
      spend: UsageMoney.fromJson(j['spend']),
      onDemand: UsageMoney.fromJson(j['onDemand']),
      shares: [
        for (final it in (shares?['items'] as List?) ?? const [])
          (
            (it as Map)['name'] as String? ?? '',
            (it['percent'] as num?)?.toDouble() ?? 0,
          ),
      ],
      sharesAt: _ms(shares?['at']),
      resets: (resets?['count'] as num?)?.toInt(),
      resetsExpireAt: _ms(resets?['expiresAt']),
      resetsAt: _ms(resets?['at']),
    );
  }

  /// 状态栏、首页卡片用的总额度窗口。
  List<UsageWindow> get mainWindows =>
      windows.where((w) => w.name.isEmpty).toList();
}

/// 详情里窗口的名字：分池带上池名，"Fable 每周"、"Auto 每月"。
String windowName(UsageWindow w) => w.name.isEmpty
    ? windowLabel(w.minutes)
    : '${w.name} ${windowLabel(w.minutes)}';

String _money(double cents) => '\$${(cents / 100).toStringAsFixed(2)}';

/// 详情里窗口下面的几行，和桌面端 planFacts 一致：套餐内金额、按需用量、本周用量用在哪、重置卡。
/// 和整家的读数不是同一份的，后面写上是多久前的。
List<String> usageFacts(AgentUsage u, DateTime now) {
  String stale(DateTime? at) =>
      at != null && u.updatedAt.difference(at) > const Duration(minutes: 10)
      ? '（${agoText(at, now)}的读数）'
      : '';
  final out = <String>[];
  final sp = u.spend;
  if (sp != null && sp.limit > 0) {
    out.add('套餐内已用 ${_money(sp.used)}，额度 ${_money(sp.limit)}');
  }
  final od = u.onDemand;
  if (od != null) {
    final limit = od.limit > 0 ? '，上限 ${_money(od.limit)}' : '';
    out.add('按需用量 ${_money(od.used)}$limit${stale(od.at)}');
  }
  if (u.shares.isNotEmpty) {
    final parts = [for (final (n, p) in u.shares) '$n ${p.round()}%'];
    out.add('本周用量用在 ${parts.join('，')}${stale(u.sharesAt)}');
  }
  final rs = u.resets;
  if (rs != null) {
    final exp = u.resetsExpireAt;
    final when = rs > 0 && exp != null
        ? '，最早一张 ${exp.month}月${exp.day}日 过期'
        : '';
    out.add('重置卡 $rs 张$when${stale(u.resetsAt)}');
  }
  return out;
}

List<AgentUsage> parseUsages(Map<String, Object?> res) => [
  for (final u in (res['usages'] as List?) ?? const [])
    AgentUsage.fromJson((u as Map).cast<String, Object?>()),
];

/// 窗口的名字，和桌面端 windowLabel 一致。
String windowLabel(int minutes) {
  const day = 24 * 60;
  if (minutes == 0) return '本期';
  if (minutes >= 295 && minutes <= 305) return '5 小时';
  if (minutes >= 7 * day - 10 && minutes <= 7 * day + 10) return '每周';
  if (minutes >= 27 * day && minutes <= 32 * day) return '每月';
  if (minutes >= day) return '${(minutes + day ~/ 2) ~/ day} 天';
  return '${(minutes + 30) ~/ 60} 小时';
}

/// "2 小时 15 分钟后重置"；超过一天写日期。
String resetText(DateTime? at, DateTime now) {
  if (at == null) return '';
  final d = at.difference(now);
  if (d <= Duration.zero) return '已重置';
  if (d < const Duration(hours: 1)) return '${d.inMinutes + 1} 分钟后重置';
  if (d < const Duration(hours: 24)) {
    final m = d.inMinutes % 60;
    return m == 0 ? '${d.inHours} 小时后重置' : '${d.inHours} 小时 $m 分钟后重置';
  }
  final hh = at.hour.toString().padLeft(2, '0');
  final mm = at.minute.toString().padLeft(2, '0');
  return '${at.month}月${at.day}日 $hh:$mm 重置';
}

String agoText(DateTime t, DateTime now) {
  final d = now.difference(t);
  if (d < const Duration(minutes: 1)) return '刚刚';
  if (d < const Duration(hours: 1)) return '${d.inMinutes} 分钟前';
  if (d < const Duration(days: 1)) return '${d.inHours} 小时前';
  return '${d.inDays} 天前';
}

String agentTitle(String agent) => switch (agent) {
  'claude' => 'Claude',
  'codex' => 'Codex',
  'grok' => 'Grok',
  'cursor' => 'Cursor',
  _ => agent,
};

/// 用到这里用红色：快要被限流了。
const usageUrgent = 80.0;

/// 显示剩余而不是已用，和桌面端各记各的，存在手机上。
final usageRemaining = ValueNotifier<bool>(false);
const _remainingKey = 'usage.remaining';

Future<void> loadUsageMode() async {
  final prefs = await SharedPreferences.getInstance();
  usageRemaining.value = prefs.getBool(_remainingKey) ?? false;
}

Future<void> setUsageRemaining(bool remaining) async {
  usageRemaining.value = remaining;
  final prefs = await SharedPreferences.getInstance();
  await prefs.setBool(_remainingKey, remaining);
}

/// 界面上写的数：已用，或者切到剩余时的 100 减已用。快用完的判断不随切换变。
double shownPercent(UsageWindow w, bool remaining) =>
    remaining ? 100 - w.percent : w.percent;

/// 首页工作区列表上面的用量卡片：每家一行，各窗口一条细进度条。点开看重置时间。
/// 电脑上没有读数、或者电脑上的 Hive 太旧没有这个接口时不显示。
class UsageCard extends StatefulWidget {
  const UsageCard({super.key, required this.model});

  final HiveAppModel model;

  @override
  State<UsageCard> createState() => _UsageCardState();
}

class _UsageCardState extends State<UsageCard> {
  List<AgentUsage> _usages = const [];
  Timer? _timer;
  Object? _rpc;

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_onModel);
    _onModel();
    unawaited(loadUsageMode());
    // 读数只在电脑上的 agent 运行时变，一分钟取一次足够
    _timer = Timer.periodic(const Duration(minutes: 1), (_) => _load());
  }

  @override
  void dispose() {
    widget.model.removeListener(_onModel);
    _timer?.cancel();
    super.dispose();
  }

  void _onModel() {
    final rpc = widget.model.rpc;
    if (identical(rpc, _rpc)) return;
    _rpc = rpc;
    if (rpc == null) {
      if (_usages.isNotEmpty) setState(() => _usages = const []);
      return;
    }
    _load();
  }

  Future<void> _load() async {
    final rpc = widget.model.rpc;
    if (rpc == null) return;
    try {
      final res = await rpc.call('usage.get', {});
      if (!mounted || !identical(rpc, widget.model.rpc)) return;
      setState(() => _usages = parseUsages(res));
    } catch (_) {
      // 旧版电脑端没有这个接口：不显示卡片
      if (mounted && _usages.isNotEmpty) setState(() => _usages = const []);
    }
  }

  @override
  Widget build(BuildContext context) => UsageCardView(usages: _usages);
}

/// 卡片的样子，和取数据分开，测试和截图直接给读数。
class UsageCardView extends StatelessWidget {
  const UsageCardView({super.key, required this.usages});

  final List<AgentUsage> usages;

  @override
  Widget build(BuildContext context) {
    final shown = usages.where((u) => u.windows.isNotEmpty).toList();
    if (shown.isEmpty) return const SizedBox.shrink();
    final c = HiveColors.of(context);
    return ValueListenableBuilder(
      valueListenable: usageRemaining,
      builder: (context, remaining, _) => _card(context, c, shown, remaining),
    );
  }

  Widget _card(
    BuildContext context,
    HiveColors c,
    List<AgentUsage> shown,
    bool remaining,
  ) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(10, 0, 10, 10),
      child: Material(
        color: c.card,
        borderRadius: BorderRadius.circular(Rd.card),
        clipBehavior: Clip.antiAlias,
        child: InkWell(
          onTap: () => showModalBottomSheet<void>(
            context: context,
            showDragHandle: true,
            isScrollControlled: true,
            useSafeArea: true,
            // 内容多时会铺满全屏：上面留一截遮罩能点掉，拖动把手也不会躲到状态栏下面
            constraints: BoxConstraints(
              maxHeight: MediaQuery.sizeOf(context).height * 0.85,
            ),
            builder: (_) => UsageSheet(usages: shown),
          ),
          child: Padding(
            padding: const EdgeInsets.fromLTRB(14, 9, 10, 9),
            child: Row(
              children: [
                Expanded(
                  child: Wrap(
                    spacing: 16,
                    runSpacing: 6,
                    children: [
                      for (final u in shown)
                        _UsageChip(usage: u, remaining: remaining),
                    ],
                  ),
                ),
                Icon(Icons.chevron_right_rounded, size: 18, color: c.faint),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

/// 卡片里的一家，和桌面状态栏一样只写一个窗口：图标、窗口简称、百分比。有 5 小时就是它。
class _UsageChip extends StatelessWidget {
  const _UsageChip({required this.usage, required this.remaining});

  final AgentUsage usage;
  final bool remaining;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final main = usage.mainWindows;
    final pool = main.isEmpty ? usage.windows : main;
    final w = pool.firstWhere(
      (w) => windowLabel(w.minutes) == '5 小时',
      orElse: () => pool.first,
    );
    final urgent = w.percent >= usageUrgent;
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        AgentTile(agent: usage.agent, size: 16),
        const SizedBox(width: 6),
        Text(
          windowShort(w.minutes),
          style: TextStyle(fontSize: Fs.caption, color: c.muted),
        ),
        const SizedBox(width: 3),
        Text(
          '${shownPercent(w, remaining).round()}%',
          style: TextStyle(
            fontSize: Fs.caption,
            fontWeight: FontWeight.w500,
            color: urgent ? c.danger : c.text,
            fontFeatures: const [FontFeature.tabularFigures()],
          ),
        ),
      ],
    );
  }
}

/// 窗口简称，和桌面状态栏一致：5h、周、月。
String windowShort(int minutes) => switch (windowLabel(minutes)) {
  '5 小时' => '5h',
  '每周' => '周',
  '每月' => '月',
  final other => other,
};

/// 进度条：画已用或剩余的那一段，陶土色，快用完时变红。
class UsageBar extends StatelessWidget {
  const UsageBar({
    super.key,
    required this.percent,
    required this.urgent,
    this.height = 6,
  });

  final double percent;
  final bool urgent;
  final double height;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final p = (percent / 100).clamp(0.0, 1.0);
    return ClipRRect(
      borderRadius: BorderRadius.circular(height / 2),
      child: Stack(
        children: [
          Container(height: height, color: c.fill),
          if (p > 0)
            FractionallySizedBox(
              widthFactor: p < 0.02 ? 0.02 : p,
              child: Container(
                height: height,
                color: urgent ? c.danger : c.accent,
              ),
            ),
        ],
      ),
    );
  }
}

/// 点开卡片的详情：每家的套餐、各窗口的进度和重置时间、读数时间。
class UsageSheet extends StatelessWidget {
  const UsageSheet({super.key, required this.usages});

  final List<AgentUsage> usages;

  @override
  Widget build(BuildContext context) => ValueListenableBuilder(
    valueListenable: usageRemaining,
    builder: (context, remaining, _) => _sheet(context, remaining),
  );

  Widget _sheet(BuildContext context, bool remaining) {
    final c = HiveColors.of(context);
    final now = DateTime.now();
    return SafeArea(
      top: false,
      child: SingleChildScrollView(
        padding: const EdgeInsets.fromLTRB(20, 0, 20, 16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Row(
              children: [
                Expanded(
                  child: Text(
                    '套餐用量',
                    style: TextStyle(
                      fontSize: Fs.cardTitle,
                      fontWeight: FontWeight.w600,
                      color: c.text,
                    ),
                  ),
                ),
                SizedBox(
                  width: 128,
                  child: Segmented(
                    labels: const ['已用', '剩余'],
                    selected: remaining ? 1 : 0,
                    onChanged: (i) => setUsageRemaining(i == 1),
                  ),
                ),
                IconButton(
                  tooltip: '关闭',
                  visualDensity: VisualDensity.compact,
                  onPressed: () => Navigator.of(context).pop(),
                  icon: Icon(Icons.close_rounded, size: 20, color: c.muted),
                ),
              ],
            ),
            for (final u in usages) ...[
              const SizedBox(height: 18),
              Row(
                children: [
                  AgentTile(agent: u.agent, size: 22),
                  const SizedBox(width: 10),
                  Text(
                    agentTitle(u.agent),
                    style: TextStyle(
                      fontSize: Fs.body,
                      fontWeight: FontWeight.w500,
                      color: c.text,
                    ),
                  ),
                  if (u.plan.isNotEmpty) ...[
                    const SizedBox(width: 8),
                    Expanded(
                      child: Text(
                        u.plan,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: TextStyle(fontSize: Fs.meta, color: c.muted),
                      ),
                    ),
                  ] else
                    const Spacer(),
                  Text(
                    agoText(u.updatedAt, now),
                    style: TextStyle(fontSize: Fs.caption, color: c.muted),
                  ),
                ],
              ),
              for (final w in u.windows) ...[
                const SizedBox(height: 12),
                Row(
                  children: [
                    Expanded(
                      child: Text(
                        windowName(w),
                        style: TextStyle(fontSize: Fs.meta, color: c.text),
                      ),
                    ),
                    Text(
                      // 分池和总额度不是同一份读数（Claude 的 Fable 只在电脑上看 /usage 时更新）
                      w.at != null &&
                              u.updatedAt.difference(w.at!) >
                                  const Duration(minutes: 10)
                          ? '${resetText(w.resetsAt, now)}，${agoText(w.at!, now)}的读数'
                          : resetText(w.resetsAt, now),
                      style: TextStyle(fontSize: Fs.caption, color: c.muted),
                    ),
                  ],
                ),
                const SizedBox(height: 6),
                Row(
                  children: [
                    Expanded(
                      child: UsageBar(
                        percent: shownPercent(w, remaining),
                        urgent: w.percent >= usageUrgent,
                      ),
                    ),
                    SizedBox(
                      width: 76,
                      child: Text(
                        '${remaining ? '剩余' : '已用'} ${shownPercent(w, remaining).round()}%',
                        textAlign: TextAlign.end,
                        style: TextStyle(
                          fontSize: Fs.meta,
                          fontWeight: FontWeight.w500,
                          color: w.percent >= usageUrgent ? c.danger : c.text,
                          fontFeatures: const [FontFeature.tabularFigures()],
                        ),
                      ),
                    ),
                  ],
                ),
              ],
              for (final f in usageFacts(u, now)) ...[
                const SizedBox(height: 8),
                Text(
                  f,
                  style: TextStyle(fontSize: Fs.caption, color: c.muted),
                ),
              ],
            ],
            const SizedBox(height: 20),
            Text(
              '电脑上的 agent 运行时顺带更新；Cursor 用量和 Codex 重置卡本地没有，在它们用过之后电脑会查一次。',
              style: TextStyle(
                fontSize: Fs.caption,
                color: c.muted,
                height: 1.5,
              ),
            ),
          ],
        ),
      ),
    );
  }
}
