@echo off
REM ==========================================================================
REM  build.bat — Windows 上无法交叉编译 iOS 二进制
REM  iOS (GOOS=ios) 需要 CGO + Xcode iOS SDK，仅 macOS 支持
REM  请在 Mac 上运行 build.sh 编译
REM ==========================================================================

echo ============================================
echo  错误: iOS 二进制不能在 Windows 上编译
echo ============================================
echo.
echo iOS 目标 (GOOS=ios) 强制要求 CGO 链接 iOS 系统库，
echo 需要 Xcode 提供的 iOS SDK，只能在 macOS 上编译。
echo.
echo 正确的编译命令 (在 Mac 终端执行):
echo.
echo   cd goios-ondevice
echo   CGO_ENABLED=1 GOOS=ios GOARCH=arm64 ^
echo       go build -ldflags="-s -w" -o goios .
echo.
echo 或直接运行: bash build.sh
echo.
echo 之前的 GOOS=darwin (macOS) 二进制在 iOS 上无法运行
echo (dyld: Library not loaded: /usr/lib/libSystem.B.dylib)
echo ============================================
pause
exit /b 1
