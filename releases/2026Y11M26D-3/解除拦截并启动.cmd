@echo off
chcp 65001 >nul
cd /d "%~dp0"
rem 双击时把自己最小化，别把黑窗口摆在用户面前
if /I not "%~1"=="min" (
  start "" /min cmd /c ""%~f0" min"
  exit /b
)
rem 自动匹配 CPU 架构：ARM64 用原生 arm64 版，x64 用主程序，32 位系统用 x86 版
set "ARCH=%PROCESSOR_ARCHITEW6432%"
if not defined ARCH set "ARCH=%PROCESSOR_ARCHITECTURE%"
set "EXE=U盘格式化工具-2026Y11M26D-2.exe"
if /I "%ARCH%"=="ARM64" set "EXE=U盘格式化工具-2026Y11M26D-2-arm64.exe"
if /I "%ARCH%"=="x86" set "EXE=U盘格式化工具-2026Y11M26D-2-x86.exe"
if not exist "%EXE%" set "EXE=U盘格式化工具-2026Y11M26D-2.exe"
echo 当前架构：%ARCH%  使用：%EXE%
echo 如果程序没有立刻出现，请等 3~5 秒（首次要初始化界面）。本窗口可关闭。
rem 三个架构的 exe 一起解除「来自网络」的拦截，省得换机器再点一次
powershell -NoProfile -Command "Get-ChildItem -Path '.\*.exe' | Unblock-File -ErrorAction SilentlyContinue; Start-Process -FilePath '.\%EXE%' -Verb RunAs"
rem 程序已在后台启动；本窗口等 3 秒自动关闭，避免「闪一下」的错觉
timeout /t 3 >nul
