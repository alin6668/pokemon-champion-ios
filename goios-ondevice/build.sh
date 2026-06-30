#!/bin/bash
# ==========================================================================
#  build.sh — 在 macOS 上编译 goios-ondevice 为 iOS arm64 二进制
#  前提: 需要安装 Xcode (提供 iOS SDK)
# ==========================================================================

set -e
cd "$(dirname "$0")"

echo "[1/3] 下载依赖..."
go mod tidy

echo "[2/3] 编译 iOS arm64 二进制 (需要 Xcode)..."
CGO_ENABLED=1 GOOS=ios GOARCH=arm64 \
    go build -ldflags="-s -w" -o goios .

echo "[3/3] 完成!"
if [ -f goios ]; then
    ls -lh goios
    file goios
    echo ""
    echo "部署步骤:"
    echo "  1. 将 goios 复制到 IPA 的 resources/ 目录（与主程序同目录）"
    echo "  2. 或 scp goios root@设备IP:/usr/local/bin/"
    echo "  3. AutoGo 脚本自动从 Documents/ 或 /usr/local/bin/ 发现 goios"
fi
