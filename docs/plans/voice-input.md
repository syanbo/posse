# 手机端语音输入转可编辑草稿与终端草稿栏方案

在 Posse 手机端（iOS / Android）提供「语音输入转可编辑草稿」能力，并在主力终端页配套折叠式「草稿输入栏（Staged Input）」，同步打通对话页与讨论页。

## 关键决策

### 1. 一期入口与终端页草稿输入栏（零 Mac 端协议改动）
- **终端页（第一优先）**：
  - 在 `mobile/lib/settings/custom_keys.dart` 的 `builtinBarKeys` 中新增内置键 `voice`（默认排在 `paste` 之后，老用户经 `normalizeBarKeys` 自动插入）。
  - 终端页底部 `TerminalKeyBar` 上方提供折叠式「草稿输入栏（Staged Input）」（样式同 `ChatComposer`，`Rd.composer` 胶囊，1–6 行自适应滚动）。
  - **绝不触发 PTY resize**：草稿栏作为 `Stack` 覆盖层贴在 `TerminalKeyBar` 正上方，遵循 `terminal.dart` 的 `resizeToAvoidBottomInset: false`，将草稿栏实际高度叠加进 `covered` 走现有 `keyboardLift` 上抬终端画面，不缩减终端 `height`。
  - **双发送动作**：主按钮「发送」（走 `sendTerminal(text: text, enter: true)`，自动享受服务端原子检查括号粘贴 + 120ms 回车），次动作「仅填入」（仅允许安全单行纯文本，拒绝换行与所有控制字符含 Tab/ESC，走 `sendTerminal(raw: bytes, enter: false)` 等待回执确认）。
  - **协同与防丢**：展开草稿栏时 `TerminalKeyBar` 的 `Esc` / `Tab` / `^C` / 方向键仍直发 PTY；回形针上传的文件路径在草稿栏展开时插入草稿光标处；设置页提供「常驻语音与草稿输入栏」开关（默认开启）：开启时常驻展示草稿栏（隐藏收起按钮）并隐藏工具栏里的 `voice` 键；关闭时恢复工具栏 `voice` 键可随时折叠/展开，收起保留内存草稿并在键帽标圆点。
- **对话页与讨论页**：
  - 输入胶囊常驻 `34×34` 麦克风按钮；对话页按来源会话归属防切会话串台（A→B→A 回到原会话时写回当前输入框），且断线重连态仍允许系统离线与手机直连云端预录草稿；讨论页遵循既有只读/断线隐藏输入区边界，锁定态（`pending != null`）禁用录音与重试并将已完成的在途转写结果暂存至解锁后合并。

### 2. 一期 ASR 引擎（双轨）
- **系统本机识别（默认零配置）**：iOS 使用 `SFSpeechRecognizer`（`.record` + `.measurement` 音频会话，优先离线并在离线包未就绪时自动回退 Apple 系统语音服务，支持 `contextualStrings` <= 100 词与自动标点，55 秒软上限）；Android API 31+ 使用 `createOnDeviceSpeechRecognizer`（Android 13+ 经 `EXTRA_BIASING_STRINGS` 传提示词），无离线服务时提示前往语音设置切云端。
- **云端 BYOK 整段上传（高准确率主力，默认强制 HTTPS，可选显式开启局域网明文 HTTP）**：
  - **OpenAI 兼容 `/v1/audio/transcriptions`**：一套 `multipart/form-data` 客户端覆盖 OpenAI（`gpt-4o-mini-transcribe` / `gpt-4o-transcribe`）、Groq（`whisper-large-v3-turbo`）、硅基流动（`FunAudioLLM/SenseVoiceSmall`，不传 `prompt`）及自定义网关，支持 `prompt` 注入当前项目名、分支名、Agent 名与用户自定义词表。
  - **火山引擎豆包录音极速版 HTTP**：`POST https://openspeech.bytedance.com/api/v3/auc/bigmodel/recognize/flash`（`X-Api-Key` + `X-Api-Resource-Id: volc.bigasr.auc_turbo` + UUID 请求 ID，先校验响应头 `X-Api-Status-Code` 区分成功/静音/业务错），国内中英混杂最稳；当前不传 OpenAI `prompt` 与语言覆盖。
- **录音格式分流**：OpenAI 兼容录 M4A（AAC-LC 16kHz mono 32kbps），豆包极速版录 WAV（16kHz 16-bit mono），客户端零转码依赖。

### 3. 原生实现、音频协同与安全边界
- **自研轻量原生插件**：`VoicePlugin.swift` / `VoicePlugin.kt`，统一 `posse/voice`、`posse/voice/events`、`posse/voice/secret`。
- **iOS `AVAudioSession` 协同**：`BackgroundPlugin.swift` 增加 `audioClaimed` 协调锁；录音期间跳过 `willResignActive` 的 `.playback` 静音保活；App 真正进入后台或发生来电中断时，立即安全落盘保留已录音频供重试，再释放锁并按需恢复保活。
- **权限按需申请**：点击麦克风时才申请最小权限集合（云端模式 iOS 不弹语音识别权限框）。
- **防丢话**：音频先落盘到不备份缓存目录，仅在转写成功写回、用户丢弃或新开录音时删除（冷启动清理 >24 小时残留）；网络或 API 失败时在输入区上方出原地「重试 / 丢弃」条。
- **API Key 安全**：独立 Keychain（`kSecAttrAccessibleWhenUnlockedThisDeviceOnly`）与 Android Keystore AES-256-GCM + `noBackupFilesDir`，绝不进 `SharedPreferences` 与系统备份。

### 4. 二期规划（不在一期阻塞）
- Mac 端新增 `posse.voice.v1` 能力，复用 `posse.bulk.v1` 流控上传音频，经 `c.async` 异步调用可插拔外部 CLI（`whisper-cli` / `mlx_whisper`）/ 本地服务或复用 Mac 代理代调云端；同时补齐对话页草稿落盘。

## 一期实施步骤

1. **双端原生插件与音频协同**：`VoicePlugin.swift`、`BackgroundPlugin.swift`、`AppDelegate.swift`、`Info.plist` + `InfoPlist.strings`；`VoicePlugin.kt`、`MainActivity.kt`、`AndroidManifest.xml`。
2. **Flutter 语音核心层（`mobile/lib/voice/`）**：原生通道、安全密钥存储、ASR 适配器与 Prompt Hint、状态机控制器、符合 `mobile-design.md` 的录音/转写/重试条组件与单元测试。
3. **终端页折叠式草稿输入栏**：`custom_keys.dart` 内置 `voice` 键、`terminal.dart` 覆盖层草稿框与 `keyboardLift` 联动、Widget 测试。
4. **对话页与讨论页接入**：`chat/composer.dart` & `chat.dart`、`discussions/room_composer.dart` & `room.dart` 接入与 Widget 测试。
5. **设置页、多语言、规则测试与文档**：`settings/voice_settings_screen.dart`、`settings.dart`、`app_state.dart`、ARB 译文与 `gen-l10n`、`fvm flutter analyze` & `fvm flutter test` 全绿、更新 `docs/features/mobile.md`。
