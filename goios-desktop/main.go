// ==========================================================================
// goios-desktop — macOS 桌面端 iOS 设备控制工具（免越狱）
//
// 通过 usbmuxd (USB) 与 iOS 设备通信，无需越狱即可控制设备。
// 命令接口与 goios-ondevice 一致，方便在越狱/非越狱场景间切换。
//
// 用法:
//   goios list                             列出已连接设备
//   goios [--udid <UDID>] <command> [...]  执行命令（默认选第一个 USB 设备）
//
// 前提: 设备需通过 USB 连接，且已信任此电脑（首次需点击"信任"）
// ==========================================================================

package main

import (
	"flag"
	"fmt"
	"os"
)

var (
	flagUDID = flag.String("udid", "", "指定设备 UDID（默认自动选第一个 USB 设备）")
)

func main() {
	flag.Usage = printUsage
	flag.Parse()

	if flag.NArg() == 0 {
		printUsage()
		os.Exit(1)
	}

	cmd := flag.Arg(0)
	args := flag.Args()[1:]

	// 特殊命令：不需要设备连接
	switch cmd {
	case "list":
		cmdListDevices()
		return
	case "help", "-h", "--help":
		printUsage()
		return
	}

	// 所有其他命令：先建立设备连接
	if err := setupDevice(*flagUDID); err != nil {
		fatalf("连接设备失败: %v\n  请确保: 1) iOS 设备已 USB 连接  2) 设备已解锁  3) 已点击\"信任此电脑\"", err)
	}
	defer teardownDevice()

	switch cmd {
	case "assistivetouch", "at", "touch":
		cmdAssistiveTouch(args)
	case "device":
		cmdDevice(args)
	case "screenshot", "ss":
		cmdScreenshot(args)
	case "app":
		cmdApp(args)
	case "ps":
		cmdPS()
	case "battery":
		cmdBattery()
	case "location", "loc", "gps":
		cmdLocation(args)
	case "image", "mount":
		cmdImage(args)
	case "file":
		cmdFile(args)
	case "sysinfo", "sys":
		cmdSysinfo()
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`goios-desktop — macOS 桌面端 iOS 设备控制工具（免越狱）

用法:
  goios list                             列出已连接设备
  goios [--udid <UDID>] <command> [...]  执行命令

命令:
  assistivetouch on|off|status    触控辅助开关/状态
  device info|name|pair           设备信息/名称/配对
  screenshot [output.png]         截图 (默认 stdout)
  app list|launch|kill <id>       应用管理
  ps                              进程列表  (需开发者镜像)
  battery                         电池信息
  location set <lat> <lon>        模拟定位  (需开发者镜像)
  location reset                  重置定位  (需开发者镜像)
  image auto                      挂载开发者镜像
  image status                    查询镜像挂载状态
  file list <bundleID> [path]     列举应用沙盒文件
  sysinfo                         系统信息

前提:
  - iOS 设备需通过 USB 连接至本 Mac
  - 首次使用需在设备上点击"信任此电脑"
  - ps/location/image 等命令需挂载开发者磁盘镜像 (goios image auto)

与 goios-ondevice 的区别:
  - goios-ondevice: 在越狱设备上运行，直连本地 lockdownd
  - goios-desktop: 在 Mac 上运行，通过 USB 连接任意 iOS 设备（免越狱）
`)
}
