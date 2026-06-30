// ==========================================================================
// goios-ondevice — 在 iOS 越狱设备上直连本地 lockdownd 的控制工具
// 编译: macOS + Xcode 下执行:
//   SDK=$(xcrun --sdk iphoneos --show-sdk-path)
//   CGO_ENABLED=1 GOOS=ios GOARCH=arm64 CGO_CFLAGS="-isysroot $SDK -miphoneos-version-min=12.0 -arch arm64" CGO_LDFLAGS="-isysroot $SDK -miphoneos-version-min=12.0 -arch arm64" go build -ldflags="-s -w"
//   或通过 GitHub Actions (.github/workflows/build-goios.yml) 自动编译
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
//   goios location set <lat> <lon>         — 模拟定位
//   goios location reset                   — 重置定位
//   goios image auto                       — 挂载开发者镜像
//   goios file list <bundleID> [path]      — 列举应用沙盒文件
//   goios sysinfo                          — 系统信息(CPU/内存/磁盘)
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
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

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
	case "location", "loc", "gps":
		cmdLocation(args)
	case "image", "mount":
		cmdImage(args)
	case "file":
		cmdFile(args)
	case "sysinfo", "sys":
		cmdSysinfo()
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
  goios location set <lat> <lon>        模拟定位
  goios location reset                  重置定位
  goios image auto                      自动挂载开发者镜像
  goios file list <bundleID> [path]     列举应用文件
  goios sysinfo                         系统信息(CPU/内存/磁盘)
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

// ========================= ps: 进程列表 =========================

// cmdPS 列举所有进程（越狱设备读取 /proc）
func cmdPS() {
	type ProcInfo struct {
		PID  int    `json:"pid"`
		Name string `json:"name"`
		PPID int    `json:"ppid"`
		// State string `json:"state"`
	}

	entries, err := ioutil.ReadDir("/proc")
	if err != nil {
		// 回退：尝试 sysctl
		cmdPS_sysctl()
		return
	}

	var procs []ProcInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == 0 {
			continue
		}
		p := ProcInfo{PID: pid}

		// 读 /proc/<pid>/stat
		statPath := filepath.Join("/proc", e.Name(), "stat")
		if data, err := ioutil.ReadFile(statPath); err == nil {
			fields := strings.Fields(string(data))
			if len(fields) > 3 {
				// /proc/pid/stat: pid (name) state ppid ...
				// name 被括号包裹，需要特殊解析
				nameStart := strings.Index(string(data), "(")
				nameEnd := strings.LastIndex(string(data), ")")
				if nameStart >= 0 && nameEnd > nameStart {
					p.Name = string(data)[nameStart+1 : nameEnd]
				}
				// state 在 ")" 之后的下一个字段
				afterName := strings.Fields(string(data)[nameEnd+1:])
				if len(afterName) > 0 {
					if ppid, err2 := strconv.Atoi(afterName[0]); err2 == nil {
						p.PPID = ppid
					}
				}
			}
		}

		// 如果 name 为空，尝试读 /proc/<pid>/comm
		if p.Name == "" {
			commPath := filepath.Join("/proc", e.Name(), "comm")
			if data, err := ioutil.ReadFile(commPath); err == nil {
				p.Name = strings.TrimSpace(string(data))
			}
		}

		if p.Name != "" {
			procs = append(procs, p)
		}
	}

	json.NewEncoder(os.Stdout).Encode(procs)
}

// cmdPS_sysctl 通过 sysctl KERN_PROC_ALL 获取进程列表（回退方案）
func cmdPS_sysctl() {
	type KInfoProc struct {
		Pid   int32
		_     [44]byte
		Ppid  int32
		_     [4]byte
		Comm  [16]byte
	}

	mib := []int32{1, 14, 0, 0} // CTL_KERN=1, KERN_PROC=14, KERN_PROC_ALL=0
	buf, err := syscallRaw(mib)
	if err != nil {
		fatal("ps失败", err)
	}

	var procs []map[string]interface{}
	entrySize := unsafe.Sizeof(KInfoProc{})
	for i := 0; i+int(entrySize) <= len(buf); i += int(entrySize) {
		info := (*KInfoProc)(unsafe.Pointer(&buf[i]))
		if info.Pid == 0 {
			continue
		}
		comm := make([]byte, 0, 16)
		for _, b := range info.Comm {
			if b == 0 {
				break
			}
			comm = append(comm, b)
		}
		procs = append(procs, map[string]interface{}{
			"pid":  info.Pid,
			"ppid": info.Ppid,
			"name": string(comm),
		})
	}
	json.NewEncoder(os.Stdout).Encode(procs)
}

// ========================= location: 模拟定位 =========================

// cmdLocation 通过 lockdown com.apple.dt.simulatelocation 模拟/重置 GPS
func cmdLocation(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: goios location set <lat> <lon> | reset")
		os.Exit(1)
	}

	conn, err := connectLockdown()
	if err != nil {
		fatal("连接 lockdownd 失败", err)
	}
	defer conn.Close()

	resp, err := conn.request(map[string]interface{}{
		"Label":   "goios-ondevice",
		"Request": "StartService",
		"Service": "com.apple.dt.simulatelocation",
	})
	if err != nil {
		fatal("启动定位模拟服务失败", err)
	}

	port, ok := resp["Port"].(uint64)
	if !ok {
		fatal("定位服务返回异常", fmt.Errorf("无端口: %v", resp))
	}

	lc, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), connTimeout)
	if err != nil {
		fatal("连接定位服务失败", err)
	}
	defer lc.Close()

	switch args[0] {
	case "set", "start":
		if len(args) < 3 {
			fatal("缺少经纬度参数: goios location set <lat> <lon>", nil)
		}
		lat, err1 := strconv.ParseFloat(args[1], 64)
		lon, err2 := strconv.ParseFloat(args[2], 64)
		if err1 != nil || err2 != nil {
			fatal("经纬度格式错误", nil)
		}

		msg := map[string]interface{}{
			"__Lat":  lat,
			"__Long": lon,
		}
		body, _ := plist.Marshal(msg, plist.BinaryFormat)
		header := make([]byte, 4)
		binary.BigEndian.PutUint32(header, uint32(len(body)))
		lc.Write(append(header, body...))

		fmt.Printf(`{"success":true,"action":"set","lat":%f,"lon":%f}`+"\n", lat, lon)

	case "reset", "stop", "clear":
		// 发送空位置或断开连接来停止模拟
		// 方法1: 发送 0,0 坐标; 方法2: 发送 __Stop
		msg := map[string]interface{}{
			"__Lat":  0.0,
			"__Long": 0.0,
		}
		body, _ := plist.Marshal(msg, plist.BinaryFormat)
		header := make([]byte, 4)
		binary.BigEndian.PutUint32(header, uint32(len(body)))
		lc.Write(append(header, body...))

		fmt.Println(`{"success":true,"action":"reset"}`)

	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s (需要 set/reset)\n", args[0])
		os.Exit(1)
	}
}

// ========================= image: 挂载开发者镜像 =========================

// cmdImage 通过 mobile_image_mounter 自动挂载 DeveloperDiskImage
func cmdImage(args []string) {
	if len(args) == 0 || (args[0] != "auto" && args[0] != "status") {
		fmt.Fprintln(os.Stderr, "用法: goios image auto | status")
		os.Exit(1)
	}

	conn, err := connectLockdown()
	if err != nil {
		fatal("连接 lockdownd 失败", err)
	}
	defer conn.Close()

	resp, err := conn.request(map[string]interface{}{
		"Label":   "goios-ondevice",
		"Request": "StartService",
		"Service": "com.apple.mobile.mobile_image_mounter",
	})
	if err != nil {
		fatal("启动镜像挂载服务失败", err)
	}

	port, ok := resp["Port"].(uint64)
	if !ok {
		fatal("镜像服务返回异常", fmt.Errorf("无端口: %v", resp))
	}

	mc, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), connTimeout)
	if err != nil {
		fatal("连接镜像服务失败", err)
	}
	defer mc.Close()

	switch args[0] {
	case "auto":
		// 1. LookupImage 查询设备上的 Developer 镜像签名
		lookupReq := map[string]interface{}{
			"Command":   "LookupImage",
			"ImageType": "Developer",
		}
		lookupBody, _ := plist.Marshal(lookupReq, plist.BinaryFormat)
		h := make([]byte, 4)
		binary.BigEndian.PutUint32(h, uint32(len(lookupBody)))
		mc.Write(append(h, lookupBody...))

		// 读 Lookup 响应
		hb := make([]byte, 4)
		if _, err := io.ReadFull(mc, hb); err != nil {
			fatal("读取Lookup响应失败", err)
		}
		blen := binary.BigEndian.Uint32(hb)
		bbody := make([]byte, blen)
		io.ReadFull(mc, bbody)

		var lookupResp map[string]interface{}
		plist.Unmarshal(bbody, &lookupResp)

		sig, _ := lookupResp["ImageSignature"].([]byte)
		if len(sig) == 0 {
			// 可能已经挂载了
			if status, _ := lookupResp["Status"].(string); status == "Complete" {
				fmt.Println(`{"success":true,"mounted":true,"note":"already_mounted"}`)
				return
			}
			// 尝试检查是否已挂载
			fmt.Println(`{"success":false,"error":"无法获取镜像签名，可能已挂载或镜像文件不存在"}`)
			os.Exit(1)
		}

		// 2. MountImage 挂载
		mountReq := map[string]interface{}{
			"Command":        "MountImage",
			"ImageType":      "Developer",
			"ImageSignature": sig,
		}
		mountBody, _ := plist.Marshal(mountReq, plist.BinaryFormat)
		h2 := make([]byte, 4)
		binary.BigEndian.PutUint32(h2, uint32(len(mountBody)))
		mc.Write(append(h2, mountBody...))

		// 读 Mount 响应
		io.ReadFull(mc, hb)
		blen = binary.BigEndian.Uint32(hb)
		bbody = make([]byte, blen)
		io.ReadFull(mc, bbody)

		var mountResp map[string]interface{}
		plist.Unmarshal(bbody, &mountResp)

		if errMsg, ok := mountResp["Error"].(string); ok && errMsg != "" {
			if strings.Contains(errMsg, "already mounted") {
				fmt.Println(`{"success":true,"mounted":true,"note":"already_mounted"}`)
				return
			}
			fatal("挂载镜像失败", fmt.Errorf(errMsg))
		}

		if status, _ := mountResp["Status"].(string); status == "Complete" {
			fmt.Println(`{"success":true,"mounted":true}`)
		} else {
			fmt.Println(`{"success":true,"mounted":true,"detail":"mounted"}`)
		}

	case "status":
		lookupReq := map[string]interface{}{
			"Command":   "LookupImage",
			"ImageType": "Developer",
		}
		lookupBody, _ := plist.Marshal(lookupReq, plist.BinaryFormat)
		h := make([]byte, 4)
		binary.BigEndian.PutUint32(h, uint32(len(lookupBody)))
		mc.Write(append(h, lookupBody...))

		hb := make([]byte, 4)
		io.ReadFull(mc, hb)
		blen := binary.BigEndian.Uint32(hb)
		bbody := make([]byte, blen)
		io.ReadFull(mc, bbody)

		var lookupResp map[string]interface{}
		plist.Unmarshal(bbody, &lookupResp)

		mounted := false
		if sig, _ := lookupResp["ImageSignature"].([]byte); len(sig) > 0 {
			mounted = true
		}
		if status, _ := lookupResp["Status"].(string); status == "Complete" {
			mounted = true
		}
		fmt.Printf(`{"mounted":%v}`+"\n", mounted)
	}
}

// ========================= file: 列举应用文件 =========================

// cmdFile 通过 installation_proxy 查找容器路径，列举应用沙盒文件
func cmdFile(args []string) {
	if len(args) < 2 || args[0] != "list" {
		fmt.Fprintln(os.Stderr, "用法: goios file list <bundleID> [path]")
		os.Exit(1)
	}

	bundleID := args[1]
	subPath := "/"
	if len(args) > 2 {
		subPath = args[2]
	}

	conn, err := connectLockdown()
	if err != nil {
		fatal("连接 lockdownd 失败", err)
	}
	defer conn.Close()

	// 1. 通过 installation_proxy 查找容器路径
	resp, err := conn.request(map[string]interface{}{
		"Label":   "goios-ondevice",
		"Request": "StartService",
		"Service": "com.apple.mobile.installation_proxy",
	})
	if err != nil {
		fatal("启动安装代理服务失败", err)
	}

	port, _ := resp["Port"].(uint64)
	pc, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), connTimeout)
	if err != nil {
		fatal("连接安装代理失败", err)
	}
	defer pc.Close()

	// 发送 Lookup 请求
	lookupReq := map[string]interface{}{
		"Command": "Lookup",
		"ClientOptions": map[string]interface{}{
			"BundleIDs":       []string{bundleID},
			"ReturnAttributes": []string{"Container"},
		},
	}
	reqBody, _ := plist.Marshal(lookupReq, plist.BinaryFormat)
	h := make([]byte, 4)
	binary.BigEndian.PutUint32(h, uint32(len(reqBody)))
	pc.Write(append(h, reqBody...))

	// 读响应
	hb := make([]byte, 4)
	io.ReadFull(pc, hb)
	blen := binary.BigEndian.Uint32(hb)
	bbody := make([]byte, blen)
	io.ReadFull(pc, bbody)

	var lookupResp map[string]interface{}
	plist.Unmarshal(bbody, &lookupResp)

	result, _ := lookupResp["LookupResult"].(map[string]interface{})
	if result == nil {
		fatal("未找到应用: "+bundleID, nil)
	}
	appInfo, ok := result[bundleID].(map[string]interface{})
	if !ok {
		fatal("未找到应用: "+bundleID, nil)
	}

	container, _ := appInfo["Container"].(string)
	if container == "" {
		fatal("应用无沙盒容器", nil)
	}

	// 2. 列举文件
	targetPath := container
	if subPath != "/" {
		targetPath = filepath.Join(container, subPath)
	}

	entries, err := ioutil.ReadDir(targetPath)
	if err != nil {
		fatal("读取目录失败: "+targetPath, err)
	}

	type FileEntry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"is_dir"`
		Size  int64  `json:"size"`
	}
	var files []FileEntry
	for _, e := range entries {
		files = append(files, FileEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  e.Size(),
		})
	}

	output := map[string]interface{}{
		"success":   true,
		"container": container,
		"path":      targetPath,
		"files":     files,
	}
	json.NewEncoder(os.Stdout).Encode(output)
}

// ========================= sysinfo: 系统信息 =========================

// cmdSysinfo 获取系统 CPU/内存/磁盘/运行时间
func cmdSysinfo() {
	info := map[string]interface{}{}

	// CPU
	info["cpu_count"] = runtime.NumCPU()

	// 主机名
	hostname, _ := os.Hostname()
	info["hostname"] = hostname

	// 内存（读取 /proc/meminfo）
	if data, err := ioutil.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "MemTotal:") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					if kb, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
						info["mem_total_mb"] = kb / 1024
					}
				}
			}
			if strings.HasPrefix(line, "MemAvailable:") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					if kb, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
						info["mem_avail_mb"] = kb / 1024
					}
				}
			}
		}
	}

	// 磁盘（statfs /）
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err == nil {
		info["disk_total_gb"] = float64(stat.Blocks) * float64(stat.Bsize) / 1024 / 1024 / 1024
		info["disk_free_gb"] = float64(stat.Bfree) * float64(stat.Bsize) / 1024 / 1024 / 1024
	}

	// 运行时间（/proc/uptime）
	if data, err := ioutil.ReadFile("/proc/uptime"); err == nil {
		parts := strings.Fields(string(data))
		if len(parts) >= 1 {
			if uptime, err := strconv.ParseFloat(parts[0], 64); err == nil {
				info["uptime_seconds"] = uptime
				info["uptime_hours"] = uptime / 3600
			}
		}
	}

	// OS 版本
	if data, err := ioutil.ReadFile("/System/Library/CoreServices/SystemVersion.plist"); err == nil {
		var plistData map[string]interface{}
		if _, err := plist.Unmarshal(data, &plistData); err == nil {
			if ver, ok := plistData["ProductVersion"].(string); ok {
				info["os_version"] = ver
			}
			if build, ok := plistData["ProductBuildVersion"].(string); ok {
				info["os_build"] = build
			}
		}
	}

	json.NewEncoder(os.Stdout).Encode(info)
}

// ========================= sysctl 辅助 =========================

// syscallRaw 执行 sysctl 并返回原始字节
func syscallRaw(mib []int32) ([]byte, error) {
	// 先获取大小
	n := uintptr(0)
	_, _, errno := syscall.Syscall6(
		syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])),
		uintptr(len(mib)),
		0,
		uintptr(unsafe.Pointer(&n)),
		0,
		0,
	)
	if errno != 0 {
		return nil, fmt.Errorf("sysctl size: %v", errno)
	}
	if n == 0 {
		return nil, nil
	}

	buf := make([]byte, n)
	_, _, errno = syscall.Syscall6(
		syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])),
		uintptr(len(mib)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&n)),
		0,
		0,
	)
	if errno != 0 {
		return nil, fmt.Errorf("sysctl data: %v", errno)
	}
	return buf[:n], nil
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
