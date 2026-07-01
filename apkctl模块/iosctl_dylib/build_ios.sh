#!/bin/bash
# build_ios.sh — 编译 libiosctl.dylib (iOS arm64)
# 需要在 macOS 上运行，需要 Xcode 和 ldid
# 用法: bash build_ios.sh

set -e

SDK_PATH=$(xcrun --sdk iphoneos --show-sdk-path)
echo "SDK: $SDK_PATH"

echo "==> 编译 libiosctl.dylib (arm64) ..."
xcrun --sdk iphoneos clang \
    -arch arm64 \
    -isysroot "$SDK_PATH" \
    -miphoneos-version-min=15.0 \
    -dynamiclib \
    -current_version 1.0.0 \
    -compatibility_version 1.0.0 \
    -o libiosctl.dylib \
    iosctl.m \
    -framework Foundation \
    -framework UIKit \
    -framework IOKit \
    -framework CoreGraphics \
    -framework AVFoundation \
    -framework CoreFoundation \
    -fobjc-arc \
    -O2

echo "==> 伪签名 ..."
ldid -S libiosctl.dylib

echo "==> 验证 ..."
file libiosctl.dylib
xcrun vtool -show libiosctl.dylib 2>/dev/null || true

echo ""
echo "===== 编译完成 ====="
echo "产物: libiosctl.dylib"
echo ""
echo "部署步骤:"
echo "  1. 复制到项目 resources:"
echo "     cp libiosctl.dylib ../resources/libs/ios/"
echo ""
echo "  2. 部署到设备 (越狱):"
echo "     scp libiosctl.dylib root@设备IP:/var/jb/usr/local/lib/"
echo ""
echo "  3. Go cgo 链接 (已有桥接文件 go_bridge.go):"
echo "     #cgo LDFLAGS: -L../../resources/libs/ios -liosctl"
echo ""
echo "  4. 在设备上测试:"
echo "     ssh root@设备IP DYLD_LIBRARY_PATH=/var/jb/usr/local/lib \\"
echo "       /path/to/app 2>&1 | grep iosctl"
