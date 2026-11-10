# 安全问题 / Security

Posse 能在手机上操作 Mac 的终端和桌面，安全问题请私下报告，不要开公开的 issue。

**怎么报告**：在 GitHub 仓库的 Security 页点「Report a vulnerability」，写清影响的版本、复现步骤和影响范围。我们会尽快回复，修好、发布新版后再公开细节。

**范围**：
- Mac 端的常驻进程、窗口和 `posse` CLI。
- 手机和 Mac 之间的配对、端到端加密（E2EE v2：X25519、HKDF、secretbox）和 QUIC 传输（`internal/mobile`、`internal/mquic`）。
- 看桌面的采集、加密和输入注入（`internal/desk`）。
- 外网中转的鉴权、邀请和推送（`internal/deskrelay`）。中转只转发密文，看不到内容；能绕过鉴权白用中转、或从中转拿到明文的都算。
- Posse 手机 App（`mobile/`）。

**不算**：要先拿到已解锁的 Mac 或手机的用户权限才能做到的事。

---

Posse can drive a Mac’s terminals and desktop from a phone, so please report security issues privately rather than in a public issue.

**How to report**: on the repository’s Security tab, click “Report a vulnerability” and include the affected version, steps to reproduce and the impact. We’ll reply as soon as we can and disclose details after a fixed release is out.

**In scope**:
- The Mac daemon, window and `posse` CLI.
- Pairing, end-to-end encryption (E2EE v2: X25519, HKDF, secretbox) and the QUIC transport between phone and Mac (`internal/mobile`, `internal/mquic`).
- Desktop capture, encryption and input injection for remote desktop (`internal/desk`).
- Relay authentication, invites and push (`internal/deskrelay`). The relay only forwards ciphertext; anything that lets someone use a relay without authorization or read plaintext through it counts.
- The Posse mobile app (`mobile/`).

**Out of scope**: anything that first requires user-level access to an unlocked Mac or phone.
