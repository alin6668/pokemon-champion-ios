// ==========================================================================
// commands.go — 所有命令实现（桌面版，通过 USB lockdown）
// ==========================================================================

package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"howett.net/plist"
)

// ────────────────────────────────────
//  工具
// ────────────────────────────────────

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "错误: "+format+"\n", args...)
	os.Exit(1)
}

// sendRecvPlist 发送/接收一次 plist（4字节BE长度 + plist）
func sendRecvPlist(conn net.Conn, msg map[string]interface{}) (map[string]interface{}, error) {
	body, err := plist.Marshal(msg, plist.BinaryFormat)
	if err != nil {
		return nil, err
	}
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(body)))
	if _, err := conn.Write(append(hdr, body...)); err != nil {
		return nil, err
	}

	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	rhdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, rhdr); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(rhdr)
	if length > 50*1024*1024 {
		return nil, fmt.Errorf("响应过大: %d", length)
	}
	rbody := make([]byte, length)
	if _, err := io.ReadFull(conn, rbody); err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if _, err := plist.Unmarshal(rbody, &result); err != nil {
		return nil, fmt.Errorf("plist 解码失败: %w", err)
	}
	return result, nil
}

// ────────────────────────────────────
//  list: 列出设备
// ────────────────────────────────────

type deviceEntry struct {
	UDID           string `json:"udid"`
	DeviceID       int    `json:"device_id"`
	ConnectionType string `json:"connection_type"`
	DeviceName     string `json:"device_name,omitempty"`
	ProductType    string `json:"product_type,omitempty"`
	OSVersion      string `json:"os_version,omitempty"`
}

func cmdListDevices() {
	devices, err := usbmuxdListDevices()
	if err != nil {
		fatalf("列出设备失败: %v", err)
	}
	if len(devices) == 0 {
		fmt.Println("[]")
		return
	}

	var list []deviceEntry
	for _, d := range devices {
		props, _ := d["Properties"].(map[string]interface{})
		de := deviceEntry{
			DeviceID: int(d["DeviceID"].(uint64)),
		}
		if props != nil {
			de.UDID, _ = props["SerialNumber"].(string)
			if de.UDID == "" {
				de.UDID, _ = props["UniqueDeviceID"].(string)
			}
			de.ConnectionType, _ = props["ConnectionType"].(string)
			if de.ConnectionType == "" {
				de.ConnectionType = "USB"
			}
			de.DeviceName, _ = props["DeviceName"].(string)
			de.ProductType, _ = props["ProductType"].(string)
			de.OSVersion, _ = props["ProductVersion"].(string)
		}
		list = append(list, de)
	}
	json.NewEncoder(os.Stdout).Encode(list)
}

// ────────────────────────────────────
//  assistivetouch: 触控辅助
// ────────────────────────────────────

const (
	accessibilityDomain = "com.apple.Accessibility"
	assistiveTouchKey   = "AssistiveTouchEnabledByiTunes"
)

func cmdAssistiveTouch(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: goios assistivetouch on|off|status")
		os.Exit(1)
	}

	switch args[0] {
	case "on", "enable", "1":
		_, err := ldConn.request(map[string]interface{}{
			"Label":   "goios-desktop",
			"Request": "SetValue",
			"Domain":  accessibilityDomain,
			"Key":     assistiveTouchKey,
			"Value":   true,
		})
		if err != nil {
			fatalf("开启 AssistiveTouch 失败: %v", err)
		}
		fmt.Println(`{"success":true,"action":"enabled"}`)

	case "off", "disable", "0":
		_, err := ldConn.request(map[string]interface{}{
			"Label":   "goios-desktop",
			"Request": "SetValue",
			"Domain":  accessibilityDomain,
			"Key":     assistiveTouchKey,
			"Value":   false,
		})
		if err != nil {
			fatalf("关闭 AssistiveTouch 失败: %v", err)
		}
		fmt.Println(`{"success":true,"action":"disabled"}`)

	case "status", "state", "get":
		resp, err := ldConn.request(map[string]interface{}{
			"Label":   "goios-desktop",
			"Request": "GetValue",
			"Domain":  accessibilityDomain,
			"Key":     assistiveTouchKey,
		})
		if err != nil {
			fatalf("查询 AssistiveTouch 状态失败: %v", err)
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
		fatalf("未知子命令: %s (需要 on/off/status)", args[0])
	}
}

// ────────────────────────────────────
//  device: 设备信息
// ────────────────────────────────────

func cmdDevice(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: goios device info|name|pair")
		os.Exit(1)
	}

	switch args[0] {
	case "info", "all":
		info := map[string]interface{}{}
		keys := []string{
			"DeviceName", "ProductType", "ProductVersion",
			"BuildVersion", "UniqueDeviceID", "SerialNumber",
			"HardwareModel", "CPUArchitecture", "DeviceColor",
			"ModelNumber", "BluetoothAddress", "WiFiAddress",
			"InternationalMobileEquipmentIdentity",
		}
		for _, key := range keys {
			resp, err := ldConn.request(map[string]interface{}{
				"Label":   "goios-desktop",
				"Request": "GetValue",
				"Key":     key,
			})
			if err != nil {
				continue
			}
			if v, ok := resp["Value"]; ok && v != nil {
				info[key] = v
			}
		}
		json.NewEncoder(os.Stdout).Encode(info)

	case "name":
		resp, err := ldConn.request(map[string]interface{}{
			"Label":   "goios-desktop",
			"Request": "GetValue",
			"Key":     "DeviceName",
		})
		if err != nil {
			fatalf("获取设备名称失败: %v", err)
		}
		if name, ok := resp["Value"].(string); ok {
			fmt.Println(name)
		} else {
			fmt.Println(resp["Value"])
		}

	case "pair":
		// 强制重新配对
		sessionID = ""
		savePairRecord("", nil) // 清除当前设备配对记录
		fmt.Println(`{"success":true,"note":"请重新连接设备以触发配对"}`)

	default:
		fatalf("未知子命令: %s (需要 info/name/pair)", args[0])
	}
}

// ────────────────────────────────────
//  screenshot: 截图
// ────────────────────────────────────

func cmdScreenshot(args []string) {
	conn, err := ldConn.startService("com.apple.mobile.screenshotr")
	if err != nil {
		fatalf("启动截图服务失败: %v", err)
	}
	defer conn.Close()

	// 1. 读取服务器主动发送的 DLMessageVersionExchange
	initResp, err := recvPlistOnly(conn)
	if err != nil {
		fatalf("读取截图服务握手失败: %v", err)
	}

	// 2. 回复版本交换
	msgType, _ := initResp["MessageType"].(string)
	if msgType == "DLMessageVersionExchange" {
		sendPlistOnly(conn, map[string]interface{}{
			"MessageType": "DLMessageVersionExchange",
			"Versions":    []int{300, 200, 100},
		})
	} else {
		// 非标准流程，尝试直接发送版本交换
		sendPlistOnly(conn, map[string]interface{}{
			"MessageType": "VersionExchange",
			"Versions":    []int{300, 200},
		})
	}

	// 3. 读取 DLMessageDeviceReady
	readyResp, err := recvPlistOnly(conn)
	if err != nil {
		fatalf("读取就绪通知失败: %v", err)
	}
	_ = readyResp

	// 4. 发送截图请求（DLMessageProcessMessage 带空数据）
	sendPlistOnly(conn, map[string]interface{}{
		"MessageType": "ScreenshotRequest",
	})

	// 5. 读取响应 — 包含 PNG 数据的 plist
	resp, err := recvPlistOnly(conn)
	if err != nil {
		fatalf("读取截图响应失败: %v", err)
	}

	// 6. 提取 PNG 数据
	pngData := extractPNGFromResponse(resp)
	if len(pngData) == 0 {
		// 可能直接返回了原始 PNG（以长度前缀）
		if raw, ok := resp["ScreenShotData"].([]uint8); ok {
			pngData = raw
		}
	}
	if len(pngData) == 0 {
		fatalf("截图响应中未找到图片数据: %v", resp)
	}

	// 7. 输出
	if len(args) > 0 && args[0] != "-" {
		if err := os.WriteFile(args[0], pngData, 0644); err != nil {
			fatalf("保存截图失败: %v", err)
		}
		fmt.Printf(`{"success":true,"file":"%s","size":%d}`+"\n", args[0], len(pngData))
	} else {
		encoded := base64.StdEncoding.EncodeToString(pngData)
		fmt.Printf(`{"success":true,"base64":"%s","size":%d}`+"\n", encoded, len(pngData))
	}
}

// extractPNGFromResponse 从 lockdown plist 响应中提取 PNG 数据
func extractPNGFromResponse(resp map[string]interface{}) []byte {
	// 尝试 ScreenShotData
	if data, ok := resp["ScreenShotData"].([]byte); ok && len(data) > 0 {
		return data
	}
	if data, ok := resp["ScreenShotData"].([]uint8); ok && len(data) > 0 {
		return data
	}
	// 尝试 PNGData
	if data, ok := resp["PNGData"].([]byte); ok && len(data) > 0 {
		return data
	}
	// 尝试 ImageData
	if data, ok := resp["ImageData"].([]byte); ok && len(data) > 0 {
		return data
	}
	return nil
}

// ────────────────────────────────────
//  app: 应用管理
// ────────────────────────────────────

func cmdApp(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: goios app list|launch <bundleID>|kill <bundleID>")
		os.Exit(1)
	}

	switch args[0] {
	case "list":
		cmdAppList()
	case "launch":
		if len(args) < 2 {
			fatalf("用法: goios app launch <bundleID>", nil)
		}
		cmdAppLaunch(args[1])
	case "kill":
		if len(args) < 2 {
			fatalf("用法: goios app kill <bundleID>", nil)
		}
		cmdAppKill(args[1])
	default:
		fatalf("未知子命令: %s (需要 list/launch/kill)", args[0])
	}
}

func cmdAppList() {
	conn, err := ldConn.startService("com.apple.mobile.installation_proxy")
	if err != nil {
		fatalf("启动安装代理失败: %v", err)
	}
	defer conn.Close()

	// 发送 Browse 请求
	browseReq := map[string]interface{}{
		"Command": "Browse",
		"ClientOptions": map[string]interface{}{
			"ApplicationType": "User",
			"ReturnAttributes": []string{
				"CFBundleIdentifier", "CFBundleDisplayName",
				"CFBundleVersion", "CFBundleShortVersionString",
			},
		},
	}
	if err := sendPlistOnly(conn, browseReq); err != nil {
		fatalf("发送 Browse 请求失败: %v", err)
	}

	var apps []map[string]interface{}
	for {
		resp, err := recvPlistOnly(conn)
		if err != nil {
			break
		}
		status, _ := resp["Status"].(string)
		if status == "Complete" {
			break
		}
		if list, ok := resp["CurrentList"].([]interface{}); ok {
			for _, a := range list {
				if am, ok := a.(map[string]interface{}); ok {
					app := map[string]interface{}{}
					if id, _ := am["CFBundleIdentifier"].(string); id != "" {
						app["bundle_id"] = id
					}
					if name, _ := am["CFBundleDisplayName"].(string); name != "" {
						app["name"] = name
					} else if name, _ := am["CFBundleIdentifier"].(string); name != "" {
						app["name"] = name
					}
					if ver, _ := am["CFBundleShortVersionString"].(string); ver != "" {
						app["version"] = ver
					}
					apps = append(apps, app)
				}
			}
		}
	}
	json.NewEncoder(os.Stdout).Encode(apps)
}

func cmdAppLaunch(bundleID string) {
	// 通过 springboardservices 启动
	conn, err := ldConn.startService("com.apple.springboardservices")
	if err != nil {
		// 回退：尝试通过 installation_proxy 的 openApp
		cmdAppLaunchFallback(bundleID)
		return
	}
	defer conn.Close()

	// 尝试多种 springboardservices 命令格式
	formats := []map[string]interface{}{
		{"command": "openAppWithBundleID", "bundleID": bundleID},
		{"command": "openApp", "bundleID": bundleID},
		{"command": "open", "bundleIdentifier": bundleID},
	}
	for _, req := range formats {
		resp, err := sendRecvPlist(conn, req)
		if err == nil {
			_ = resp
			fmt.Printf(`{"success":true,"action":"launched","bundle_id":"%s"}`+"\n", bundleID)
			return
		}
	}
	fatalf("启动应用失败，可能需挂载开发者镜像 (goios image auto)", nil)
}

func cmdAppLaunchFallback(bundleID string) {
	fmt.Fprintf(os.Stderr, "提示: 启动应用需要挂载开发者镜像。请先执行: goios image auto\n")
	os.Exit(1)
}

func cmdAppKill(bundleID string) {
	// 通过 springboardservices 或 instruments 杀死
	conn, err := ldConn.startService("com.apple.springboardservices")
	if err != nil {
		fatalf("启动 SpringBoard 服务失败: %v", err)
	}
	defer conn.Close()

	formats := []map[string]interface{}{
		{"command": "killApp", "bundleID": bundleID},
		{"command": "killProcessWithBundleID", "bundleID": bundleID},
	}
	for _, req := range formats {
		resp, err := sendRecvPlist(conn, req)
		if err == nil {
			_ = resp
			fmt.Printf(`{"success":true,"action":"killed","bundle_id":"%s"}`+"\n", bundleID)
			return
		}
	}
	fatalf("杀死应用失败，可能需挂载开发者镜像", nil)
}

// ────────────────────────────────────
//  ps: 进程列表
// ────────────────────────────────────

func cmdPS() {
	// 桌面端通过 instruments 获取进程列表（需 DDI）
	conn, err := ldConn.startService("com.apple.instruments.remoteserver")
	if err != nil {
		fatalf("启动 instruments 失败（需要挂载开发者镜像: goios image auto）: %v", err)
	}
	defer conn.Close()

	// instruments 协议较复杂，这里提供基础实现
	// 实际应用中应使用完整的 instruments 协议库
	fmt.Fprintf(os.Stderr, "提示: ps 需要完整的 instruments 协议支持\n")
	fmt.Println(`[]`)
}

// ────────────────────────────────────
//  battery: 电池信息
// ────────────────────────────────────

func cmdBattery() {
	conn, err := ldConn.startService("com.apple.mobile.diagnostics_relay")
	if err != nil {
		fatalf("启动诊断服务失败: %v", err)
	}
	defer conn.Close()

	// 请求电池信息
	resp, err := sendRecvPlist(conn, map[string]interface{}{
		"Request": "GetBatteryInfo",
		"Label":   "goios-desktop",
	})
	if err != nil {
		fatalf("获取电池信息失败: %v", err)
	}

	info := map[string]interface{}{}
	if v, ok := resp["IsCharging"]; ok {
		info["charging"] = v
	}
	if v, ok := resp["ExternalChargeCapable"]; ok {
		info["external_charge_capable"] = v
	}
	if v, ok := resp["BatteryCurrentCapacity"]; ok {
		info["level"] = v
	}
	if v, ok := resp["GasGaugeCapability"]; ok {
		info["gas_gauge"] = v
	}

	json.NewEncoder(os.Stdout).Encode(info)
}

// ────────────────────────────────────
//  location: 模拟定位 (需 DDI)
// ────────────────────────────────────

func cmdLocation(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: goios location set <lat> <lon> | reset")
		os.Exit(1)
	}

	conn, err := ldConn.startService("com.apple.dt.simulatelocation")
	if err != nil {
		fatalf("启动定位模拟失败（需要挂载开发者镜像）: %v", err)
	}
	defer conn.Close()

	switch args[0] {
	case "set", "start":
		if len(args) < 3 {
			fatalf("用法: goios location set <lat> <lon>", nil)
		}
		lat, err1 := strconv.ParseFloat(args[1], 64)
		lon, err2 := strconv.ParseFloat(args[2], 64)
		if err1 != nil || err2 != nil {
			fatalf("经纬度格式错误", nil)
		}

		// simulatelocation 使用固定格式
		msg := map[string]interface{}{
			"__Lat":  lat,
			"__Long": lon,
		}
		body, _ := plist.Marshal(msg, plist.BinaryFormat)
		hdr := make([]byte, 4)
		binary.BigEndian.PutUint32(hdr, uint32(len(body)))
		conn.Write(append(hdr, body...))

		fmt.Printf(`{"success":true,"action":"set","lat":%f,"lon":%f}`+"\n", lat, lon)

	case "reset", "stop", "clear":
		// 发送 0,0 重置
		msg := map[string]interface{}{
			"__Lat":  0.0,
			"__Long": 0.0,
		}
		body, _ := plist.Marshal(msg, plist.BinaryFormat)
		hdr := make([]byte, 4)
		binary.BigEndian.PutUint32(hdr, uint32(len(body)))
		conn.Write(append(hdr, body...))

		fmt.Println(`{"success":true,"action":"reset"}`)

	default:
		fatalf("未知子命令: %s (需要 set/reset)", args[0])
	}
}

// ────────────────────────────────────
//  image: 挂载开发者镜像
// ────────────────────────────────────

func cmdImage(args []string) {
	if len(args) == 0 || (args[0] != "auto" && args[0] != "status") {
		fmt.Fprintln(os.Stderr, "用法: goios image auto | status")
		os.Exit(1)
	}

	conn, err := ldConn.startService("com.apple.mobile.mobile_image_mounter")
	if err != nil {
		fatalf("启动镜像挂载失败: %v", err)
	}
	defer conn.Close()

	switch args[0] {
	case "auto":
		// 1. LookupImage
		lookupReq := map[string]interface{}{
			"Command":   "LookupImage",
			"ImageType": "Developer",
		}
		lookupResp, err := sendRecvPlist(conn, lookupReq)
		if err != nil {
			fatalf("LookupImage 失败: %v", err)
		}

		sig, _ := lookupResp["ImageSignature"].([]byte)
		if len(sig) == 0 {
			// 可能已挂载
			if status, _ := lookupResp["Status"].(string); status == "Complete" {
				fmt.Println(`{"success":true,"mounted":true,"note":"already_mounted"}`)
				return
			}
			fatalf("无法获取镜像签名。请确保 DeveloperDiskImage.dmg 已放置到设备可访问路径", nil)
		}

		// 2. MountImage
		mountReq := map[string]interface{}{
			"Command":        "MountImage",
			"ImageType":      "Developer",
			"ImageSignature": sig,
		}
		mountResp, err := sendRecvPlist(conn, mountReq)
		if err != nil {
			fatalf("MountImage 失败: %v", err)
		}

		if errMsg, ok := mountResp["Error"].(string); ok && errMsg != "" {
			if strings.Contains(errMsg, "already mounted") {
				fmt.Println(`{"success":true,"mounted":true,"note":"already_mounted"}`)
				return
			}
			fatalf("挂载失败: %s", errMsg)
		}

		fmt.Println(`{"success":true,"mounted":true}`)

	case "status":
		lookupReq := map[string]interface{}{
			"Command":   "LookupImage",
			"ImageType": "Developer",
		}
		lookupResp, err := sendRecvPlist(conn, lookupReq)
		if err != nil {
			fatalf("查询失败: %v", err)
		}
		mounted := false
		if sig, _ := lookupResp["ImageSignature"].([]byte); len(sig) > 0 {
			mounted = true
		}
		if s, _ := lookupResp["Status"].(string); s == "Complete" {
			mounted = true
		}
		fmt.Printf(`{"mounted":%v}`+"\n", mounted)
	}
}

// ────────────────────────────────────
//  file: 列举应用沙盒文件
// ────────────────────────────────────

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

	// 1. 通过 house_arrest 获取 AFC 访问权限
	conn, err := ldConn.startService("com.apple.mobile.house_arrest")
	if err != nil {
		fatalf("启动 house_arrest 失败: %v", err)
	}
	defer conn.Close()

	// 发送 VendContainer
	vcResp, err := sendRecvPlist(conn, map[string]interface{}{
		"Command":   "VendContainer",
		"Identifier": bundleID,
	})
	if err != nil {
		fatalf("VendContainer 失败: %v (应用可能未安装或不允许文件访问)", err)
	}

	// 检查状态
	if status, _ := vcResp["Status"].(string); status != "Complete" {
		if errMsg, _ := vcResp["Error"].(string); errMsg != "" {
			fatalf("无法访问应用沙盒: %s", errMsg)
		}
		fatalf("无法访问应用沙盒: %v", vcResp)
	}

	// 2. 获取容器路径并列举文件
	container, _ := vcResp["Container"].(string)
	if container == "" {
		fatalf("应用无沙盒容器", nil)
	}

	// 使用 AFC 服务列举文件
	afcConn, err := ldConn.startService("com.apple.afc")
	if err != nil {
		fatalf("启动 AFC 服务失败: %v", err)
	}
	defer afcConn.Close()

	// AFC 协议比较复杂，这里用简化方式：通过 lockdown 再次连接 house_arrest
	// 实际上 AFC 有自己的二进制协议，纯 plist 无法操作。
	// 这里返回容器路径，用户可以用其他工具浏览

	output := map[string]interface{}{
		"success":   true,
		"container": container,
		"path":      filepath.Join(container, subPath),
		"note":      "AFC 文件操作需要专门的 AFC 协议实现（使用 go-ios 或 pymobiledevice3 操作文件）",
	}
	json.NewEncoder(os.Stdout).Encode(output)
}

// ────────────────────────────────────
//  sysinfo: 系统信息
// ────────────────────────────────────

func cmdSysinfo() {
	info := map[string]interface{}{}

	// 通过 lockdown 查询多项系统信息
	sysKeys := map[string]string{
		"ProductVersion":              "os_version",
		"BuildVersion":                "os_build",
		"ProductType":                 "model",
		"DeviceName":                  "device_name",
		"HardwareModel":               "hardware_model",
		"CPUArchitecture":             "cpu_arch",
		"UniqueDeviceID":              "udid",
		"SerialNumber":                "serial",
		"DeviceColor":                 "color",
		"ModelNumber":                 "model_number",
		"InternationalMobileEquipmentIdentity": "imei",
		"ProductName":     "product_name",
		"SIMStatus":       "sim_status",
		"TimeIntervalSince1970": "unix_time",
	}

	for key, field := range sysKeys {
		resp, err := ldConn.request(map[string]interface{}{
			"Label":   "goios-desktop",
			"Request": "GetValue",
			"Key":     key,
		})
		if err != nil {
			continue
		}
		if v, ok := resp["Value"]; ok && v != nil {
			info[field] = v
		}
	}

	// 尝试获取电池信息（作为额外系统信息）
	if battConn, err := ldConn.startService("com.apple.mobile.diagnostics_relay"); err == nil {
		battResp, err := sendRecvPlist(battConn, map[string]interface{}{
			"Request": "GetBatteryInfo",
			"Label":   "goios-desktop",
		})
		if err == nil {
			if v, ok := battResp["BatteryCurrentCapacity"]; ok {
				info["battery_level"] = v
			}
			if v, ok := battResp["IsCharging"]; ok {
				info["battery_charging"] = v
			}
		}
		battConn.Close()
	}

	json.NewEncoder(os.Stdout).Encode(info)
}

// ────────────────────────────────────
//  辅助发送/接收 plist（不带 label/request 包装）
// ────────────────────────────────────

func sendPlistOnly(conn net.Conn, msg map[string]interface{}) error {
	body, err := plist.Marshal(msg, plist.BinaryFormat)
	if err != nil {
		return err
	}
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(body)))
	_, err = conn.Write(append(hdr, body...))
	return err
}

func recvPlistOnly(conn net.Conn) (map[string]interface{}, error) {
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(hdr)
	if length > 50*1024*1024 {
		return nil, fmt.Errorf("响应过大: %d", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	var result map[string]interface{}
	if _, err := plist.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return result, nil
}
