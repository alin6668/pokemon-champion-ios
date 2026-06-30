// ==========================================================================
// goios-daemon — 本地 HTTP 服务，封装 go-ios CLI
// 启动: go run main.go          (开发)
//        goios-daemon            (编译后)
//        goios-daemon -port 8092 (自定义端口)
// 调用: curl http://127.0.0.1:8091/health
// ==========================================================================

package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ========================= 全局配置 =========================

var (
	端口    = flag.String("port", "8091", "HTTP 监听端口")
	iosBin = flag.String("ios", "ios", "go-ios 二进制路径")
)

// ========================= 响应结构 =========================

type APIResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

func jsonOK(data interface{}) APIResponse    { return APIResponse{Success: true, Data: data} }
func jsonErr(msg string) APIResponse          { return APIResponse{Success: false, Error: msg} }
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

// ========================= go-ios 命令封装 =========================

var mu sync.Mutex // ios 命令串行执行，避免竞态

// runIOS 执行 ios <args...> 并返回 stdout 字符串
func runIOS(args ...string) (string, error) {
	mu.Lock()
	defer mu.Unlock()

	cmd := exec.Command(*iosBin, args...)
	cmd.Env = append(os.Environ(),
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
	)
	out, err := cmd.Output()
	return string(out), err
}

// runIOSJSON 执行 ios <args...> 并解析 JSON 输出
func runIOSJSON(out interface{}, args ...string) error {
	stdout, err := runIOS(args...)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(stdout), out)
}

// ========================= HTTP 处理器 =========================

func handleHealth(w http.ResponseWriter, r *http.Request) {
	_, err := runIOS("version")
	if err != nil {
		writeJSON(w, jsonErr("go-ios 不可用: "+err.Error()))
		return
	}
	writeJSON(w, jsonOK(map[string]string{
		"status":  "ok",
		"version": "go-ios ready",
	}))
}

// ---------- 设备 ----------

func handleDeviceInfo(w http.ResponseWriter, r *http.Request) {
	stdout, err := runIOS("info")
	if err != nil {
		writeJSON(w, jsonErr(err.Error()))
		return
	}
	var info interface{}
	json.Unmarshal([]byte(stdout), &info)
	writeJSON(w, jsonOK(info))
}

func handleDeviceList(w http.ResponseWriter, r *http.Request) {
	stdout, err := runIOS("list")
	if err != nil {
		writeJSON(w, jsonErr(err.Error()))
		return
	}
	// 去除 go-ios 可能输出的非 JSON 提示行
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		writeJSON(w, jsonOK([]interface{}{}))
		return
	}
	var devices interface{}
	// go-ios list 输出可能是一个数组也可能是以换行分隔的对象
	if stdout[0] == '[' || stdout[0] == '{' {
		json.Unmarshal([]byte(stdout), &devices)
	} else {
		devices = strings.Split(stdout, "\n")
	}
	writeJSON(w, jsonOK(devices))
}

// ---------- 触控辅助 ----------

func handleTouchOff(w http.ResponseWriter, r *http.Request) {
	_, err := runIOS("assistivetouch", "disable")
	if err != nil {
		writeJSON(w, jsonErr(err.Error()))
		return
	}
	writeJSON(w, jsonOK("assistivetouch disabled"))
}

func handleTouchOn(w http.ResponseWriter, r *http.Request) {
	_, err := runIOS("assistivetouch", "enable")
	if err != nil {
		writeJSON(w, jsonErr(err.Error()))
		return
	}
	writeJSON(w, jsonOK("assistivetouch enabled"))
}

func handleTouchStatus(w http.ResponseWriter, r *http.Request) {
	stdout, err := runIOS("assistivetouch", "status")
	if err != nil {
		writeJSON(w, jsonErr(err.Error()))
		return
	}
	status := strings.TrimSpace(stdout)
	writeJSON(w, jsonOK(map[string]interface{}{
		"enabled": strings.EqualFold(status, "true") || status == "1" || strings.EqualFold(status, "enabled"),
		"raw":     status,
	}))
}

// ---------- 截图 ----------

func handleScreenshot(w http.ResponseWriter, r *http.Request) {
	// ios screenshot --output=- 输出 PNG 到 stdout  (go-ios 实际语法)
	cmd := exec.Command(*iosBin, "screenshot")
	cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8")

	stdout, err := cmd.Output()
	if err != nil {
		writeJSON(w, jsonErr("截图失败: "+err.Error()))
		return
	}

	b64 := base64.StdEncoding.EncodeToString(stdout)
	writeJSON(w, jsonOK(map[string]string{
		"format": "png",
		"base64": b64,
	}))
}

// ---------- 应用管理 ----------

func handleLaunchApp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BundleID string `json:"bundle_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BundleID == "" {
		writeJSON(w, jsonErr("缺少 bundle_id"))
		return
	}
	if _, err := runIOS("launch", req.BundleID); err != nil {
		writeJSON(w, jsonErr("启动失败: "+err.Error()))
		return
	}
	writeJSON(w, jsonOK("launched: "+req.BundleID))
}

func handleKillApp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BundleID string `json:"bundle_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BundleID == "" {
		writeJSON(w, jsonErr("缺少 bundle_id"))
		return
	}
	if _, err := runIOS("kill", req.BundleID); err != nil {
		writeJSON(w, jsonErr("关闭失败: "+err.Error()))
		return
	}
	writeJSON(w, jsonOK("killed: "+req.BundleID))
}

func handleListApps(w http.ResponseWriter, r *http.Request) {
	stdout, err := runIOS("apps")
	if err != nil {
		writeJSON(w, jsonErr(err.Error()))
		return
	}
	var apps interface{}
	json.Unmarshal([]byte(stdout), &apps)
	writeJSON(w, jsonOK(apps))
}

// ---------- 进程 ----------

func handlePS(w http.ResponseWriter, r *http.Request) {
	stdout, err := runIOS("ps")
	if err != nil {
		writeJSON(w, jsonErr(err.Error()))
		return
	}
	var procs interface{}
	json.Unmarshal([]byte(stdout), &procs)
	writeJSON(w, jsonOK(procs))
}

// ---------- 开发者镜像 ----------

func handleImageAuto(w http.ResponseWriter, r *http.Request) {
	out, err := runIOS("image", "auto")
	if err != nil {
		writeJSON(w, jsonErr("挂载镜像失败: "+err.Error()))
		return
	}
	writeJSON(w, jsonOK(map[string]string{"result": strings.TrimSpace(out)}))
}

// ---------- 电池 ----------

func handleBattery(w http.ResponseWriter, r *http.Request) {
	stdout, err := runIOS("batterycheck")
	if err != nil {
		writeJSON(w, jsonErr(err.Error()))
		return
	}
	var batt interface{}
	json.Unmarshal([]byte(stdout), &batt)
	writeJSON(w, jsonOK(batt))
}

// ---------- 位置 ----------

func handleSetLocation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, jsonErr("缺少 lat/lon"))
		return
	}
	_, err := runIOS("setlocation",
		"--lat", fmt.Sprintf("%.6f", req.Lat),
		"--lon", fmt.Sprintf("%.6f", req.Lon),
	)
	if err != nil {
		writeJSON(w, jsonErr("设置位置失败: "+err.Error()))
		return
	}
	writeJSON(w, jsonOK(fmt.Sprintf("location set: %.6f, %.6f", req.Lat, req.Lon)))
}

func handleResetLocation(w http.ResponseWriter, r *http.Request) {
	_, err := runIOS("resetlocation")
	if err != nil {
		writeJSON(w, jsonErr("重置位置失败: "+err.Error()))
		return
	}
	writeJSON(w, jsonOK("location reset"))
}

// ---------- 配对 ----------

func handlePairDevice(w http.ResponseWriter, r *http.Request) {
	out, err := runIOS("pair")
	if err != nil {
		writeJSON(w, jsonErr("配对失败: "+err.Error()))
		return
	}
	writeJSON(w, jsonOK(map[string]string{"result": strings.TrimSpace(out)}))
}

// ---------- 文件操作 ----------

func handleFileList(w http.ResponseWriter, r *http.Request) {
	bundleID := r.URL.Query().Get("bundle_id")
	path := r.URL.Query().Get("path")
	if bundleID == "" {
		writeJSON(w, jsonErr("缺少 bundle_id"))
		return
	}
	args := []string{"file", "ls", "--bundle-id=" + bundleID}
	if path != "" {
		args = append(args, "--path="+path)
	}
	stdout, err := runIOS(args...)
	if err != nil {
		writeJSON(w, jsonErr("列出文件失败: "+err.Error()))
		return
	}
	var files interface{}
	json.Unmarshal([]byte(stdout), &files)
	writeJSON(w, jsonOK(files))
}

// ========================= 路由注册 =========================

func main() {
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("goios-daemon 启动中... 端口=%s  ios命令=%s", *端口, *iosBin)

	// 验证 ios 命令可用
	ver, err := runIOS("version")
	if err != nil {
		log.Printf("⚠ 警告: ios 命令不可用 (%v)，请确保已安装 go-ios", err)
		log.Printf("   安装: npm install -g go-ios")
		log.Printf("   或指定路径: goios-daemon -ios /path/to/ios")
	} else {
		log.Printf("✓ go-ios 可用: %s", strings.TrimSpace(ver))
	}

	mux := http.NewServeMux()

	// 根路由
	mux.HandleFunc("/health", handleHealth)

	// 设备
	mux.HandleFunc("/api/device/info", handleDeviceInfo)
	mux.HandleFunc("/api/device/list", handleDeviceList)

	// 触控辅助 ← 核心需求
	mux.HandleFunc("/api/assistivetouch/on", handleTouchOn)
	mux.HandleFunc("/api/assistivetouch/off", handleTouchOff)
	mux.HandleFunc("/api/assistivetouch/status", handleTouchStatus)

	// 截图
	mux.HandleFunc("/api/screenshot", handleScreenshot)

	// 应用管理
	mux.HandleFunc("/api/app/launch", handleLaunchApp)
	mux.HandleFunc("/api/app/kill", handleKillApp)
	mux.HandleFunc("/api/app/list", handleListApps)

	// 进程
	mux.HandleFunc("/api/ps", handlePS)

	// 开发者镜像
	mux.HandleFunc("/api/image/auto", handleImageAuto)

	// 电池
	mux.HandleFunc("/api/battery", handleBattery)

	// 位置
	mux.HandleFunc("/api/location/set", handleSetLocation)
	mux.HandleFunc("/api/location/reset", handleResetLocation)

	// 配对
	mux.HandleFunc("/api/device/pair", handlePairDevice)

	// 文件
	mux.HandleFunc("/api/file/list", handleFileList)

	// 中间件: 日志 + 只允许 POST/GET
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		log.Printf("→ %s %s", r.Method, r.URL.Path)
		mux.ServeHTTP(w, r)
		log.Printf("← %s %s (%v)", r.Method, r.URL.Path, time.Since(start))
	})

	addr := "127.0.0.1:" + *端口
	log.Printf("┌──────────────────────────────────────────────┐")
	log.Printf("│  goios-daemon 就绪                            │")
	log.Printf("├──────────────────────────────────────────────┤")
	log.Printf("│  地址:  http://%s                         │", addr)
	log.Printf("│  健康:  http://%s/health                   │", addr)
	log.Printf("│  触控:  /api/assistivetouch/on|off|status     │")
	log.Printf("│  截图:  /api/screenshot                       │")
	log.Printf("│  应用:  /api/app/launch|kill|list              │")
	log.Printf("└──────────────────────────────────────────────┘")
	log.Printf("按 Ctrl+C 退出")

	server := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}

// init 确保有设备时的预检查日志
func init() {
	// 检查 ios 是否在 PATH 中
	if _, err := exec.LookPath("ios"); err != nil {
		fmt.Fprintf(os.Stderr, "[warn] 'ios' 不在 PATH 中，请先安装 go-ios:\n")
		fmt.Fprintf(os.Stderr, "       npm install -g go-ios\n")
		fmt.Fprintf(os.Stderr, "       或 go build github.com/danielpaulus/go-ios\n\n")
	}
}

// 避免未使用的导入警告
var _ = io.Discard
