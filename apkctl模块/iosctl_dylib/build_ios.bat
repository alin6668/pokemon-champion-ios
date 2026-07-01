@echo off
REM build_ios.bat — Windows 占位脚本
REM iOS arm64 dylib 无法在 Windows 上编译，需要 Xcode + macOS
REM 请在 macOS 上运行 build_ios.sh，或使用 GitHub Actions 自动编译

echo ============================================
echo    libiosctl.dylib - Windows 占位脚本
echo ============================================
echo.
echo iOS arm64 dylib 编译要求:
echo   1. macOS + Xcode
echo   2. xcrun --sdk iphoneos
echo   3. ldid (brew install ldid)
echo.
echo 请在 macOS 终端运行:
echo   bash build_ios.sh
echo.
echo 或使用 GitHub Actions 自动编译:
echo   参考 .github/workflows/build-iosctl.yml
echo.
exit /b 1
