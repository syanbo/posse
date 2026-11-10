#!/bin/sh
# 第二阶段局域网协作的 TLS 身份固定单机试验（docs/plans/collab-tls-trial.md）：
# 本机回环地址上跑房主和成员两端，证明指纹固定、持钥验证、会话恢复时仍校验、撤销后恢复会话被拒、
# 非 Ed25519 证书被拒。只是单机协议正确性，不代替两台 Mac 的本地网络权限试验。
set -eu
cd "$(dirname "$0")/.."
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
go version
# 先落盘再筛：直接接管道的话退出码是 grep 的，测试失败也照样返回 0
rc=0
go test ./internal/collaboration -run 'TestTLS|TestIdentity|TestRoomKey' -count=1 -v >/tmp/posse-collab-tls-trial.log 2>&1 || rc=$?
grep -E '^(=== RUN|--- |ok|FAIL|PASS)' /tmp/posse-collab-tls-trial.log || true
grep -q -- '--- SKIP: TestTLSResumptionStillChecks' /tmp/posse-collab-tls-trial.log && echo "注意：这次没有发生会话恢复，恢复时的校验没有被验证到" >&2 || true
echo "完整输出：/tmp/posse-collab-tls-trial.log"
exit "$rc"
