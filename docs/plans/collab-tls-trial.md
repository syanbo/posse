# 局域网协作：TLS 单机试验与两机试验的分工

第二阶段（讨论的局域网多人协作）第 2 步要先回答两类问题：协议本身对不对（单机能验证），以及 macOS 在两台真实电脑之间放不放行、授权记在谁身上（只有两台 Mac 能回答）。本文记录前者已经验证到哪里、后者要测什么。第 3 步（两机文字协作）起依赖两类结论，不在这之前开工。

## 单机试验（已做）

`scripts/collab-tls-trial.sh`（跑 `internal/collaboration` 的测试，输出留在 `/tmp/posse-collab-tls-trial.log`）。两端都在本机回环地址上：

| 验证点 | 做法 | 测试 |
|---|---|---|
| 安装身份稳定、并发首次创建只有一份、0600 | `collab.json` 原子写入，已存在不覆盖 | `TestIdentityStable`、`TestIdentityConcurrentCreate` |
| 客户端按邀请里的指纹固定房主 | `InsecureSkipVerify` 只跳过 CA 链，`VerifyConnection` 比对证书公钥 SPKI 指纹；指纹不对握手失败 | `TestTLSPinsBothSides` |
| 服务端要求持钥、拿到对端指纹 | `ClientAuth = RequireAnyClientCert`，`VerifyConnection` 取指纹交给应用层；不出示证书的客户端被拒 | `TestTLSPinsBothSides` |
| 会话恢复不绕过校验 | 第二次连接 `DidResume` 为真时服务端的检查照样被调用；撤销后用缓存的会话重连被拒 | `TestTLSResumptionStillChecks`（没发生恢复时测试会 Skip，脚本会提示） |
| 非安装身份的密钥连不上 | ECDSA 自签证书被拒；未知但合法的 Ed25519 身份能握手，指纹交给应用层（只开放加入申请由应用层决定） | `TestTLSRejectsForeignKeys` |

结论只覆盖传输身份。谁能读哪个房间、邀请、撤销、代际（membership generation）都在应用层，属于第 3 步之后，现在没有实现。

## 依赖清单

### 单机多进程能验证的（协议正确性，第 3 步起随实现一起测）

- 邀请：十分钟有效（按房主时钟）、单次消费的条件更新、凭据哈希保存、同一申请 ID 重取凭据不再消费邀请。
- 成员与撤销：批准绑定申请时的指纹、成员代际递增、逐请求复核撤销、旧代凭据和旧代事件拒收为终态。
- 消息与执行：`(room, author, clientMessageId)`、`(room, executionID)`、`(room, executionID, eventSeq)` 唯一和负载摘要冲突检查；本机授权意图与领取匹配；接受账本防止重复启动 worker；outbox 按至少一次补发。
- 归档、撤销与领取/发送的事务竞态；非房主归档被拒。
- 附件分块（64 KiB）、路径穿越、配额、限流、控制帧 1 MiB 与正文 256 KiB 上限、上传并发上限。
- 独立服务边界：WebSocket 路径 `/collaboration/v1` 和子协议、手机凭据和手机 RPC 进不来。
- 交接：新进程拿到 `possed.lock` 才监听协作端口，游标不回退。

这些用多个 `POSSE_HOME`、多个常驻进程在回环或本机网卡地址上就能测；能证明协议对，不能证明局域网可达和系统权限。

### 只有两台真实 Mac 能确定的（第 2 步，阻塞第 3 步）

1. 本地网络权限：常驻进程从 `~/Library/Application Support/Posse/runtime/<包ID>/<版本>/` 的副本运行，拨号和监听时系统弹不弹「本地网络」授权、记在哪个程序上（副本、Posse.app 还是拉起它的终端）；拒绝后错误能否和「网络不通」区分；重装、换包、交接后授权是否还在。决定 Info.plist 要加的 `NSLocalNetworkUsageDescription`、`NSBonjourServices` 和界面上的排查提示。
2. 应用防火墙：监听协作端口时 macOS 防火墙是否询问、记在谁身上（Developer ID 签名的副本）。
3. Bonjour/mDNS：从副本发布和浏览服务是否可用；组播被屏蔽时手动粘贴邀请（带多个候选地址）是否始终可用。
4. 真实网络行为：Wi-Fi 切换、IP 变化、睡眠唤醒后重连；默认端口在真实网络上的冲突。
5. 在两台机器上复跑上面的 TLS 验证点（证书指纹固定、会话恢复撤销），确认真实链路和单机结论一致。

### 门槛

- 第二阶段第 1 步（核验首版）：第一阶段的未验证项关闭后才算通过（见任务报告）。
- 第 2 步：单机部分已做；两机部分等第二台 Mac。
- 第 3 步起：第 1、2 步都通过后再开工。
