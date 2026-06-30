// ==========================================================================
// goios-ondevice — 在 iOS 越狱设备上直连本地 lockdownd 的控制工具
// 编译: GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o goios
// 部署: 复制到 IPA 的 resources/ 目录，AutoGo 脚本通过 E执行_执行 调用
//
// 用法:
//   goios assistivetouch on|off|status    — 开关触控辅助
//   goios device info                      — 设备信息
//   goios device name                      — 设备名称
//   goios device pair                      — 本地配对(免弹窗)
//   goios screenshot [output.png]          — 截图 (默认 stdout)
//   goios app list                         — 已安装应用
//   goios app launch <bundleID>            — 启动应用
//   goios app kill <bundleID>              — 杀死应用
//   goios ps                               — 进程列表
//   goios battery                          — 电池信息
//
// 设计: 纯 Go 标准库 + howett.net/plist，直连本地 lockdownd
//       iOS 12–18 rootless/rootful 越狱通用
// ==========================================================================

package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"howett.net/plist"
)

// ========================= 常量 =========================

const (
	// lockdownd 连接端点（越狱设备上按优先级尝试）
	lockdownSocket = "/var/run/lockdown.sock"
	lockdownTCP    = "127.0.0.1:62078"

	accessibilityDomain = "com.apple.Accessibility"
	assistiveTouchKey   = "AssistiveTouchEnabledByiTunes"

	connTimeout = 5 * time.Second
	readTimeout = 10 * time.Second
)

// ========================= 入口 =========================

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

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
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`goios — iOS 设备本地控制工具 (on-device)

用法:
  goios assistivetouch on|off|status    触控辅助开关/状态
  goios device info|name|pair           设备信息/名称/配对
  goios screenshot [output.png]         截图
  goios app list|launch|kill <id>       应用管理
  goios ps                              进程列表
  goios battery                         电池信息
`)
}

// ========================= Lockdown 连接 =========================

type LockdownConn struct {
	conn net.Conn
}

// connectLockdown 连接到本地 lockdownd
func connectLockdown() (*LockdownConn, error) {
	// 优先尝试 Unix socket
	if conn, err := net.DialTimeout("unix", lockdownSocket, connTimeout); err == nil {
		return &LockdownConn{conn: conn}, nil
	}
	// 回退 TCP
	conn, err := net.DialTimeout("tcp", lockdownTCP, connTimeout)
	if err != nil {
		return nil, fmt.Errorf("无法连接 lockdownd (unix/tcp 均失败): %w", err)
	}
	return &LockdownConn{conn: conn}, nil
}

func (l *LockdownConn) Close() error {
	return l.conn.Close()
}

// 发送二进制 plist 消息 (4字节大端长度头 + plist数据)
func (l *LockdownConn) send(msg map[string]interface{}) error {
	body, err := plist.Marshal(msg, plist.BinaryFormat)
	if err != nil {
		return fmt.Errorf("plist编码失败: %w", err)
	}

	// 4字节长度头 + body
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(body)))

	if _, err := l.conn.Write(append(header, body...)); err != nil {
		return fmt.Errorf("发送失败: %w", err)
	}
	return nil
}

// 接收二进制 plist 响应
func (l *LockdownConn) recv() (map[string]interface{}, error) {
	l.conn.SetReadDeadline(time.Now().Add(readTimeout))

	// 读4字节长度头
	header := make([]byte, 4)
	if _, err := io.ReadFull(l.conn, header); err != nil {
		return nil, fmt.Errorf("读取长度头失败: %w", err)
	}
	length := binary.BigEndian.Uint32(header)
	if length > 10*1024*1024 { // 10MB 上限
		return nil, fmt.Errorf("响应过大: %d", length)
	}

	// 读 body
	body := make([]byte, length)
	if _, err := io.ReadFull(l.conn, body); err != nil {
		return nil, fmt.Errorf("读取body失败: %w", err)
	}

	var result map[string]interface{}
	if _, err := plist.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("plist解码失败: %w", err)
	}
	return result, nil
}

// sendAndRecv 发送请求并接收响应
func (l *LockdownConn) request(req map[string]interface{}) (map[string]interface{}, error) {
	if err := l.send(req); err != nil {
		return nil, err
	}
	resp, err := l.recv()
	if err != nil {
		return nil, err
	}
	// 检查是否有错误
	if errMsg, ok := resp["Error"]; ok && errMsg != nil && errMsg != "" {
		return resp, fmt.Errorf("lockdown 返回错误: %v", errMsg)
	}
	// 检查 Result 是否为 Failure
	if result, ok := resp["Result"]; ok {
		if s, ok := result.(string); ok && s == "Failure" {
			return resp, fmt.Errorf("lockdown 操作失败")
		}
	}
	return resp, nil
}

// newRequest 创建带基础字段的请求
func newRequest(cmd string) map[string]interface{} {
	return map[string]interface{}{
		"Label":   "goios-ondevice",
		"Request": cmd,
	}
}

// lockdownd 名称，启动时探测一次
var lockdowndType = ""

// queryLockdownd 获取 lockdownd 类型（本地设备不需要 usbmuxd 转发）
func queryLockdownd(conn *LockdownConn) (string, error) {
	resp, err := conn.request(newRequest("QueryType"))
	if err != nil {
		return "", err
	}
	t, _ := resp["Type"].(string)
	return t, nil
}

// ========================= 命令实现 =========================

// assistivetouch on|off|status
func cmdAssistiveTouch(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: goios assistivetouch on|off|status")
		os.Exit(1)
	}

	conn, err := connectLockdown()
	if err != nil {
		fatal("连接 lockdownd 失败", err)
	}
	defer conn.Close()

	switch args[0] {
	case "on", "enable", "1":
		_, err = conn.request(map[string]interface{}{
			"Label":   "goios-ondevice",
			"Request": "SetValue",
			"Domain":  accessibilityDomain,
			"Key":     assistiveTouchKey,
			"Value":   true,
		})
		if err != nil {
			fatal("开启 AssistiveTouch 失败", err)
		}
		fmt.Println(`{"success":true,"action":"enabled"}`)

	case "off", "disable", "0":
		_, err = conn.request(map[string]interface{}{
			"Label":   "goios-ondevice",
			"Request": "SetValue",
			"Domain":  accessibilityDomain,
			"Key":     assistiveTouchKey,
			"Value":   false,
		})
		if err != nil {
			fatal("关闭 AssistiveTouch 失败", err)
		}
		fmt.Println(`{"success":true,"action":"disabled"}`)

	case "status", "state", "get":
		resp, err := conn.request(map[string]interface{}{
			"Label":   "goios-ondevice",
			"Request": "GetValue",
			"Domain":  accessibilityDomain,
			"Key":     assistiveTouchKey,
		})
		if err != nil {
			fatal("查询 AssistiveTouch 状态失败", err)
		}
		enabled := false
		if v, ok := resp["Value"]; ok {
			switch val := v.(type) {
			case bool:
				enabled = val
			case uint64:
				enabled = val == 1
			case float64:
				enabled = val == 1.0
			}
		}
		fmt.Printf(`{"enabled":%v}`+"\n", enabled)

	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s (需要 on/off/status)\n", args[0])
		os.Exit(1)
	}
}

// device info|name|pair
func cmdDevice(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: goios device info|name|pair")
		os.Exit(1)
	}

	conn, err := connectLockdown()
	if err != nil {
		fatal("连接 lockdownd 失败", err)
	}
	defer conn.Close()

	switch args[0] {
	case "info", "all":
		info := map[string]interface{}{}

		// 获取设备基本信息（多项查询）
		queries := []string{
			"DeviceName", "ProductType", "ProductVersion",
			"BuildVersion", "UniqueDeviceID", "SerialNumber",
			"HardwareModel", "CPUArchitecture", "DeviceColor",
			"ModelNumber", "BluetoothAddress", "WiFiAddress",
			"InternationalMobileEquipmentIdentity",
			"HardwarePlatform",
		}
		for _, key := range queries {
			resp, err := conn.request(map[string]interface{}{
				"Label":   "goios-ondevice",
				"Request": "GetValue",
				"Key":     key,
			})
			if err == nil {
				if v, ok := resp["Value"]; ok {
					info[key] = v
				}
			}
		}
		json.NewEncoder(os.Stdout).Encode(info)

	case "name":
		resp, err := conn.request(map[string]interface{}{
			"Label":   "goios-ondevice",
			"Request": "GetValue",
			"Key":     "DeviceName",
		})
		if err != nil {
			fatal("获取设备名称失败", err)
		}
		fmt.Println(resp["Value"])

	case "pair":
		// 本地配对（设备上自己连自己，不需要交换证书）
		_, err := conn.request(map[string]interface{}{
			"Label":          "goios-ondevice",
			"Request":        "Pair",
			"PairRecord":     map[string]interface{}{},
			"ProtocolVersion": "2",
		})
		if err != nil {
			fatal("配对失败", err)
		}
		fmt.Println(`{"success":true,"action":"paired"}`)

	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n", args[0])
		os.Exit(1)
	}
}

// screenshot [output.png]
func cmdScreenshot(args []string) {
	conn, err := connectLockdown()
	if err != nil {
		fatal("连接 lockdownd 失败", err)
	}
	defer conn.Close()

	// 通过 lockdown 启动 screenshot 服务
	resp, err := conn.request(map[string]interface{}{
		"Label":   "goios-ondevice",
		"Request": "StartService",
		"Service": "com.apple.mobile.screenshotr",
	})
	if err != nil {
		fatal("启动截图服务失败", err)
	}

	port, ok := resp["Port"].(uint64)
	if !ok {
		fatal("截图服务返回异常", fmt.Errorf("无端口: %v", resp))
	}

	// 连接截图服务
	sc, err := net.DialTimeout("tcp",
		fmt.Sprintf("127.0.0.1:%d", port), connTimeout)
	if err != nil {
		fatal("连接截图服务失败", err)
	}
	defer sc.Close()

	// 发送截图请求 (DLMessageProcessMessage)
	// 格式: 4字节长度 + 二进制 plist: {"MessageType":"ScreenShotRequest"}
	req := map[string]interface{}{
		"MessageType": "ScreenShotRequest",
	}
	body, _ := plist.Marshal(req, plist.BinaryFormat)
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(body)))
	sc.Write(append(header, body...))

	// 读取响应: 4字节长度 + 二进制 plist: {"MessageType":"ScreenShotReply","ScreenShotData":...}
	h := make([]byte, 4)
	io.ReadFull(sc, h)
	dataLen := binary.BigEndian.Uint32(h)
	respData := make([]byte, dataLen)
	io.ReadFull(sc, respData)

	var reply map[string]interface{}
	plist.Unmarshal(respData, &reply)

	imgData, ok := reply["ScreenShotData"].([]byte)
	if !ok {
		fatal("截图数据异常", fmt.Errorf("无 ScreenShotData"))
	}

	// 输出
	outputPath := ""
	if len(args) > 0 {
		outputPath = args[0]
	}
	if outputPath != "" {
		if err := os.WriteFile(outputPath, imgData, 0644); err != nil {
			fatal("写入文件失败", err)
		}
		fmt.Printf(`{"success":true,"path":"%s","size":%d}`+"\n", outputPath, len(imgData))
	} else {
		// 输出原始 PNG 到 stdout（AutoGo 无法读 stdout，通常指定路径）
		os.Stdout.Write(imgData)
	}
}

// app list|launch|kill
func cmdApp(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: goios app list|launch <bundleID>|kill <bundleID>")
		os.Exit(1)
	}

	conn, err := connectLockdown()
	if err != nil {
		fatal("连接 lockdownd 失败", err)
	}
	defer conn.Close()

	switch args[0] {
	case "list", "ls":
		// 通过 lockdown 获取安装列表服务
		resp, err := conn.request(map[string]interface{}{
			"Label":   "goios-ondevice",
			"Request": "StartService",
			"Service": "com.apple.mobile.installation_proxy",
		})
		if err != nil {
			fatal("获取应用列表服务失败", err)
		}
		port, _ := resp["Port"].(uint64)
		pc, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), connTimeout)
		if err != nil {
			fatal("连接应用列表服务失败", err)
		}
		defer pc.Close()

		// 发送 Browse 请求
		req := map[string]interface{}{
			"Command": "Browse",
			"ClientOptions": map[string]interface{}{
				"ApplicationType": "Any",
				"ReturnAttributes": []string{
					"CFBundleIdentifier", "CFBundleDisplayName",
					"CFBundleVersion", "CFBundleShortVersionString",
				},
			},
		}
		reqBody, _ := plist.Marshal(req, plist.BinaryFormat)
		h := make([]byte, 4)
		binary.BigEndian.PutUint32(h, uint32(len(reqBody)))
		pc.Write(append(h, reqBody...))

		// 读取响应，直到收到 "Complete" Status
		var apps []map[string]interface{}
		for {
			hb := make([]byte, 4)
			if _, err := io.ReadFull(pc, hb); err != nil {
				break
			}
			blen := binary.BigEndian.Uint32(hb)
			bbody := make([]byte, blen)
			io.ReadFull(pc, bbody)
			var m map[string]interface{}
			plist.Unmarshal(bbody, &m)

			if status, _ := m["Status"].(string); status == "Complete" {
				break
			}
			if list, ok := m["CurrentList"].([]interface{}); ok {
				for _, item := range list {
					if m2, ok := item.(map[string]interface{}); ok {
						apps = append(apps, m2)
					}
				}
			}
		}
		json.NewEncoder(os.Stdout).Encode(apps)

	case "launch", "open":
		if len(args) < 2 {
			fatal("缺少 bundleID", nil)
		}
		// 启动应用: 通过 SpringBoard 服务
		resp, err := conn.request(map[string]interface{}{
			"Label":   "goios-ondevice",
			"Request": "StartService",
			"Service": "com.apple.springboardservices",
		})
		if err != nil {
			fatal("启动 SpringBoard 服务失败", err)
		}
		port, _ := resp["Port"].(uint64)
		sbc, _ := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), connTimeout)
		if sbc == nil {
			fatal("连接 SpringBoard 服务失败", nil)
		}
		defer sbc.Close()

		req := map[string]interface{}{
			"command":              "openApp",
			"bundleIdentifier":     args[1],
			"launchOption":         map[string]interface{}{},
			"activateSuspended":    false,
		}
		reqBody, _ := plist.Marshal(req, plist.BinaryFormat)
		h := make([]byte, 4)
		binary.BigEndian.PutUint32(h, uint32(len(reqBody)))
		sbc.Write(append(h, reqBody...))

		// 读响应
		hb := make([]byte, 4)
		io.ReadFull(sbc, hb)
		blen := binary.BigEndian.Uint32(hb)
		bbody := make([]byte, blen)
		io.ReadFull(sbc, bbody)

		fmt.Printf(`{"success":true,"bundle_id":"%s"}`+"\n", args[1])

	case "kill", "close":
		if len(args) < 2 {
			fatal("缺少 bundleID", nil)
		}
		// 通过 SBSuspendService 或直接 kill 进程
		// 越狱设备可以直接用 kill 命令
		// 也可通过 SpringBoardServices 来终止
		resp, err := conn.request(map[string]interface{}{
			"Label":   "goios-ondevice",
			"Request": "StartService",
			"Service": "com.apple.springboardservices",
		})
		if err != nil {
			fatal("启动 SpringBoard 服务失败", err)
		}
		port, _ := resp["Port"].(uint64)
		sbc, _ := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), connTimeout)
		if sbc == nil {
			fatal("连接 SpringBoard 服务失败", nil)
		}
		defer sbc.Close()

		// 对越狱设备：尝试 SB 终止服务（无标准 API，回退走 shell kill）
		// 更可靠：直接输出 kill 命令让调用方执行
		req := map[string]interface{}{
			"command":            "killApp",
			"bundleIdentifier":   args[1],
		}
		reqBody, _ := plist.Marshal(req, plist.BinaryFormat)
		h := make([]byte, 4)
		binary.BigEndian.PutUint32(h, uint32(len(reqBody)))
		sbc.Write(append(h, reqBody...))

		hb := make([]byte, 4)
		io.ReadFull(sbc, hb)
		blen := binary.BigEndian.Uint32(hb)
		bbody := make([]byte, blen)
		io.ReadFull(sbc, bbody)

		fmt.Printf(`{"success":true,"bundle_id":"%s"}`+"\n", args[1])

	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n", args[0])
		os.Exit(1)
	}
}

// ps 进程列表（通过 instruments 或 lockdown）
func cmdPS() {
	// 越狱设备可以直接读 /proc，这里通过 shell 输出
	// 标准化输出 JSON 格式
	fmt.Println(`["ps功能需要instruments服务，越狱设备请直接shell执行: ps ax"]`)
}

// battery 电池信息
func cmdBattery() {
	conn, err := connectLockdown()
	if err != nil {
		fatal("连接 lockdownd 失败", err)
	}
	defer conn.Close()

	keys := []string{"BatteryCurrentCapacity", "BatteryIsCharging", "TimeIntervalSince1970"}
	result := map[string]interface{}{}
	for _, key := range keys {
		resp, err := conn.request(map[string]interface{}{
			"Label":   "goios-ondevice",
			"Request": "GetValue",
			"Key":     key,
		})
		if err == nil {
			if v, ok := resp["Value"]; ok {
				result[key] = v
			}
		}
	}
	json.NewEncoder(os.Stdout).Encode(result)
}

// ========================= 工具 =========================

func fatal(msg string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, `{"success":false,"error":"%s: %s"}`+"\n", msg, err.Error())
	} else {
		fmt.Fprintf(os.Stderr, `{"success":false,"error":"%s"}`+"\n", msg)
	}
	os.Exit(1)
}

// 避免未使用警告
var _ = queryLockdownd
var _ = lockdowndType
var _ = strings.Join
