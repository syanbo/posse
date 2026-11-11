import '../app_state.dart';

/// 订阅 desk.subscribe 的参数：屏幕像素、能解的编码，和这台电脑（[key] 是它的公钥）记着的屏、手选帧率。
/// 旧电脑不认 fps，照常按码率选。
Map<String, Object?> deskSubscribeParams(
  AppSettings s,
  String? key, {
  required int width,
  required int height,
}) => {
  'width': width,
  'height': height,
  // 插件只会解 HEVC。
  'codecs': ['hevc'],
  'display': ?s.deskDisplays[key],
  'fps': ?s.deskFps[key],
};

/// 画面面板里帧率一行的当前值（0 是自动）。电脑的 ready 没带 `fps: true`（旧电脑）时为 null，不显示这一行。
int? deskFpsOffer(bool supported, AppSettings s, String? key) =>
    supported ? s.deskFps[key] ?? 0 : null;

/// 手选帧率（0 是自动）：按电脑记下，下次订阅带上；[call] 不为空时（正在看）让电脑立即换，不重新订阅。
/// 没在看了、连接断了都不报错。
Future<void> chooseDeskFps(
  PosseAppModel model,
  String? key,
  int fps, {
  Future<Object?> Function(String method, Map<String, Object?> params)? call,
}) async {
  if (fps != 0 && !deskFpsChoices.contains(fps)) fps = 0;
  if (key != null) {
    model.updateSettings((s) {
      if (fps == 0) {
        s.deskFps.remove(key);
      } else {
        s.deskFps[key] = fps;
      }
    });
  }
  if (call == null) return;
  try {
    await call('desk.fps', {'fps': fps});
  } on Object {
    // 下次订阅照记下的帧率开始
  }
}
