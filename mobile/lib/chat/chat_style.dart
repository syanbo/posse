import 'package:flutter/material.dart';

import '../ui/theme.dart';

Color chatAccent(BuildContext context) => HiveColors.of(context).accent;

Color chatMuted(BuildContext context) => HiveColors.of(context).muted;

/// 自己的话用强调色淡底。深色 20%、浅色 12%，和桌面历史详情的 tint 一致。
Color chatTint(BuildContext context) => HiveColors.of(context).tint;

Color chatCard(BuildContext context) => HiveColors.of(context).card;

/// 等你回应的卡片（批准、提问）：卡片底加琥珀色细边，是界面上唯一醒目的东西。
BoxDecoration askCard(HiveColors c) => BoxDecoration(
  color: c.card,
  borderRadius: BorderRadius.circular(Rd.card),
  border: Border.all(color: c.honey.withValues(alpha: 0.5)),
);
