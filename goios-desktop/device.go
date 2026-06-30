// ==========================================================================
// device.go — usbmuxd 连接 + 设备管理 + lockdown 会话
// ==========================================================================

package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"howett.net/plist"
)

const (
	usbmuxdSocket  = "/var/run/usbmuxd"
	lockdownPort   = 62078 // iOS 设备 lockdownd 端口
	connTimeout    = 5 * time.Second
	readTimeout    = 10 * time.Second
	sysPairDir     = "/var/db/lockdown" // macOS 系统配对记录（只读）
)

// userPairDir 返回用户配对记录目录
func userPairDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/goios"
	}
	return filepath.Join(home, ".goios", "lockdown")
}

// 全局设备连接状态
var (
	ldConn    *lockdownConn
	deviceID  int
	sessionID string // StartSession 返回的会话 ID
	hostID    string // 持久化 HostID（配对用）
)

// ────────────────────────────────────
//  usbmuxd 协议
// ────────────────────────────────────

// usbmuxdConnect 连接本地 usbmuxd 并返回连接
func usbmuxdConnect() (net.Conn, error) {
	return net.DialTimeout("unix", usbmuxdSocket, connTimeout)
}

// usbmuxdSend 发送 usbmuxd 消息 (4字节BE长度 + plist)
func usbmuxdSend(conn net.Conn, msg map[string]interface{}) error {
	body, err := plist.Marshal(msg, plist.BinaryFormat)
	if err != nil {
		return err
	}
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(body)))
	if _, err := conn.Write(append(hdr, body...)); err != nil {
		return err
	}
	return nil
}

// usbmuxdRecv 接收 usbmuxd 响应
func usbmuxdRecv(conn net.Conn) (map[string]interface{}, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(hdr)
	if length > 16*1024*1024 {
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

// usbmuxdConnectDevice 通过 usbmuxd 连接到指定设备的指定端口
// 返回已建立隧道的连接（可直接进行 lockdown 通信）
func usbmuxdConnectDevice(devID, port int) (net.Conn, error) {
	conn, err := usbmuxdConnect()
	if err != nil {
		return nil, fmt.Errorf("连接 usbmuxd 失败: %w", err)
	}

	// 发送 Connect 请求
	connectMsg := map[string]interface{}{
		"MessageType": "Connect",
		"DeviceID":    devID,
		"PortNumber":  port,
	}
	if err := usbmuxdSend(conn, connectMsg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("usbmuxd Connect 发送失败: %w", err)
	}

	// 读取响应
	resp, err := usbmuxdRecv(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("usbmuxd Connect 响应失败: %w", err)
	}

	msgType, _ := resp["MessageType"].(string)
	if msgType == "Result" {
		if num, ok := resp["Number"].(uint64); ok && num == 0 {
			return conn, nil // 隧道建立成功
		}
	}
	conn.Close()
	return nil, fmt.Errorf("usbmuxd Connect 失败: %v", resp)
}

// ────────────────────────────────────
//  设备列表
// ────────────────────────────────────

// usbmuxdListDevices 列出所有已连接设备
func usbmuxdListDevices() ([]map[string]interface{}, error) {
	conn, err := usbmuxdConnect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	msg := map[string]interface{}{
		"MessageType":         "ListDevices",
		"ClientVersionString": "goios-desktop",
		"ProgName":            "goios",
	}
	if err := usbmuxdSend(conn, msg); err != nil {
		return nil, err
	}

	resp, err := usbmuxdRecv(conn)
	if err != nil {
		return nil, err
	}

	devices, ok := resp["DeviceList"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("DeviceList 格式异常: %v", resp)
	}

	var result []map[string]interface{}
	for _, d := range devices {
		if dm, ok := d.(map[string]interface{}); ok {
			result = append(result, dm)
		}
	}
	return result, nil
}

// findDevice 根据 UDID（或自动选第一个 USB 设备）返回设备信息
func findDevice(udid string) (map[string]interface{}, error) {
	devices, err := usbmuxdListDevices()
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("未检测到已连接设备，请用 USB 连接 iOS 设备")
	}

	if udid != "" {
		for _, d := range devices {
			props, _ := d["Properties"].(map[string]interface{})
			if props == nil {
				continue
			}
			if id, _ := props["SerialNumber"].(string); id == udid {
				return d, nil
			}
			if id, _ := props["UniqueDeviceID"].(string); id == udid {
				return d, nil
			}
		}
		return nil, fmt.Errorf("未找到指定设备: %s (可用 'goios list' 查看已连接设备)", udid)
	}

	// 自动选第一个 USB 设备
	for _, d := range devices {
		props, _ := d["Properties"].(map[string]interface{})
		if props == nil {
			continue
		}
		ct, _ := props["ConnectionType"].(string)
		if ct == "USB" || ct == "" {
			return d, nil
		}
	}
	// 回退到第一个设备
	return devices[0].(map[string]interface{}), nil
}

// ────────────────────────────────────
//  lockdown 连接（复用 ondevice 的协议层）
// ────────────────────────────────────

type lockdownConn struct {
	conn net.Conn
}

// lockdownSend 发送 lockdown 消息
func (l *lockdownConn) send(msg map[string]interface{}) error {
	body, err := plist.Marshal(msg, plist.BinaryFormat)
	if err != nil {
		return err
	}
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(body)))
	if _, err := l.conn.Write(append(hdr, body...)); err != nil {
		return err
	}
	return nil
}

// lockdownRecv 接收 lockdown 响应
func (l *lockdownConn) recv() (map[string]interface{}, error) {
	l.conn.SetReadDeadline(time.Now().Add(readTimeout))
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(l.conn, hdr); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(hdr)
	if length > 10*1024*1024 {
		return nil, fmt.Errorf("响应过大: %d", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(l.conn, body); err != nil {
		return nil, err
	}
	var result map[string]interface{}
	if _, err := plist.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// request 发送请求并接收响应，检查 Error/Result（自动附带 SessionID）
func (l *lockdownConn) request(req map[string]interface{}) (map[string]interface{}, error) {
	// 桌面版必须附带 SessionID
	if sessionID != "" {
		req["SessionID"] = sessionID
	}
	if err := l.send(req); err != nil {
		return nil, err
	}
	resp, err := l.recv()
	if err != nil {
		return nil, err
	}
	if errMsg, ok := resp["Error"]; ok && errMsg != nil && errMsg != "" {
		return resp, fmt.Errorf("lockdown 错误: %v", errMsg)
	}
	if result, ok := resp["Result"].(string); ok && result == "Failure" {
		return resp, fmt.Errorf("lockdown 操作失败")
	}
	return resp, nil
}

// startService 启动 lockdown 服务并返回隧道连接
func (l *lockdownConn) startService(name string) (net.Conn, error) {
	resp, err := l.request(map[string]interface{}{
		"Label":   "goios-desktop",
		"Request": "StartService",
		"Service": name,
	})
	if err != nil {
		return nil, err
	}

	// 有些服务返回 Port + EnableServiceSSL
	if port, ok := resp["Port"].(uint64); ok {
		if ssl, _ := resp["EnableServiceSSL"].(bool); ssl {
			return nil, fmt.Errorf("服务 %s 需要 SSL，暂不支持", name)
		}
		// 通过 usbmuxd 连接此端口
		return usbmuxdConnectDevice(deviceID, int(port))
	}

	// 有些服务直接返回在同一个连接上（如 installation_proxy 有时）
	return nil, fmt.Errorf("服务 %s 未返回端口", name)
}

// ────────────────────────────────────
//  设备连接 & 会话管理
// ────────────────────────────────────

// setupDevice 连接设备并建立 lockdown 会话
func setupDevice(udid string) error {
	dev, err := findDevice(udid)
	if err != nil {
		return err
	}

	deviceID = int(dev["DeviceID"].(uint64))
	props, _ := dev["Properties"].(map[string]interface{})

	// 1. 通过 usbmuxd 连接设备 lockdownd
	conn, err := usbmuxdConnectDevice(deviceID, lockdownPort)
	if err != nil {
		return fmt.Errorf("连接 lockdownd 失败: %w", err)
	}
	ldConn = &lockdownConn{conn: conn}

	// 2. 查询 lockdownd 类型
	queryResp, err := ldConn.request(map[string]interface{}{
		"Label":   "goios-desktop",
		"Request": "QueryType",
	})
	if err != nil {
		return fmt.Errorf("查询 lockdownd 类型失败: %w", err)
	}
	lockdownType, _ := queryResp["Type"].(string)
	if lockdownType == "" {
		lockdownType = "com.apple.mobile.lockdown"
	}

	// 3. 获取设备 UDID 用于配对
	deviceUDID := udid
	if deviceUDID == "" {
		deviceUDID, _ = props["SerialNumber"].(string)
		if deviceUDID == "" {
			deviceUDID, _ = props["UniqueDeviceID"].(string)
		}
	}

	// 4. 尝试配对（读取 macOS 本地配对记录）
	pairRecord := readPairRecord(deviceUDID)
	if pairRecord != nil {
		// 已有配对记录，验证
		validateResp, err := ldConn.request(map[string]interface{}{
			"Label":          "goios-desktop",
			"Request":        "ValidatePair",
			"PairRecord":     pairRecord,
		})
		_ = validateResp
		if err != nil {
			// ValidatePair 失败，清除记录重新配对
			pairRecord = nil
		}
	}

	// 4. 加载或生成 HostID（持久化，确保多次配对使用同一 HostID）
	hostID = loadHostID()
	if hostID == "" {
		hostID = generateHostID()
		saveHostID(hostID)
	}
	buid := generateBUID()

	if pairRecord == nil {
		// 无配对记录，执行 Pair（会触发设备"信任此电脑"弹窗）
		pairResp, err := ldConn.request(map[string]interface{}{
			"Label":   "goios-desktop",
			"Request": "Pair",
			"PairRecord": map[string]interface{}{
				"DeviceCertificate":  []byte{},
				"HostCertificate":    []byte{},
				"HostID":             hostID,
				"RootCertificate":    []byte{},
				"SystemBUID":         buid,
			},
		})
		if err != nil {
			return fmt.Errorf("配对失败（请确保设备已解锁并点击\"信任\"）: %w", err)
		}
		// 保存配对记录
		if pr, ok := pairResp["PairRecord"].(map[string]interface{}); ok {
			pairRecord = pr
			savePairRecord(deviceUDID, pairRecord)
		} else {
			// Pair 返回的记录
			pairRecord = pairResp
		}
	}

	// 从已保存的配对记录中提取 HostID 和 BUID
	if savedHostID, _ := pairRecord["HostID"].(string); savedHostID != "" {
		hostID = savedHostID
	}
	if savedBUID, _ := pairRecord["SystemBUID"].(string); savedBUID != "" {
		buid = savedBUID
	}

	// 5. 开始会话
	sessionResp, err := ldConn.request(map[string]interface{}{
		"Label":      "goios-desktop",
		"Request":    "StartSession",
		"HostID":     hostID,
		"SystemBUID": buid,
	})
	if err != nil {
		return fmt.Errorf("启动会话失败: %w", err)
	}

	// 保存 SessionID（后续所有请求都需要）
	if sid, ok := sessionResp["SessionID"].(string); ok {
		sessionID = sid
	}

	return nil
}

// teardownDevice 关闭设备连接
func teardownDevice() {
	if ldConn != nil && ldConn.conn != nil {
		ldConn.conn.Close()
		ldConn = nil
	}
	sessionID = ""
}

// ────────────────────────────────────
//  HostID 持久化（避免每次配对生成新 ID）
// ────────────────────────────────────

func hostIDPath() string {
	return filepath.Join(userPairDir(), "host_id")
}

func loadHostID() string {
	data, err := os.ReadFile(hostIDPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func saveHostID(id string) {
	os.MkdirAll(userPairDir(), 0755)
	os.WriteFile(hostIDPath(), []byte(id), 0644)
}

// ────────────────────────────────────
//  配对记录管理（先读系统目录，后写用户目录）
// ────────────────────────────────────

func readPairRecord(udid string) map[string]interface{} {
	if udid == "" {
		return nil
	}
	filename := udid + ".plist"

	// 1. 先尝试系统配对目录（iTunes/Finder 生成的记录，只读）
	for _, dir := range []string{sysPairDir, userPairDir()} {
		path := filepath.Join(dir, filename)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var record map[string]interface{}
		if _, err := plist.Unmarshal(data, &record); err != nil {
			continue
		}
		return record
	}
	return nil
}

func savePairRecord(udid string, record map[string]interface{}) {
	if udid == "" {
		return
	}
	dir := userPairDir()
	os.MkdirAll(dir, 0755)
	path := filepath.Join(dir, udid+".plist")
	data, err := plist.Marshal(record, plist.XMLFormat)
	if err != nil {
		return
	}
	os.WriteFile(path, data, 0644)
}

func generateHostID() string {
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		time.Now().UnixNano()&0xFFFFFFFF,
		os.Getpid()&0xFFFF,
		time.Now().Unix()&0xFFFF,
		time.Now().Nanosecond()&0xFFFF,
		time.Now().UnixNano()&0xFFFFFFFF,
	)
}

func generateBUID() string {
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		os.Getpid()&0xFFFFFFFF,
		time.Now().Unix()&0xFFFF,
		os.Getpid()^int(time.Now().Unix())&0xFFFF,
		time.Now().Nanosecond()&0xFFFF,
		os.Getpid()*37987&0xFFFFFFFF,
	)
}
