// dylib_tester — 独立测试 libiosctl.dylib，无需 AutoGo 包
// 直接 cgo dlopen/dlsym，可单独编译为 iOS arm64 二进制
// 编译: CGO_ENABLED=1 GOOS=ios GOARCH=arm64 go build -ldflags="-s -w" .

package main

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdlib.h>
#include <stdio.h>

static void* _h = NULL;

// 函数指针
static int    (*_respring)(void);
static int    (*_reboot)(void);
static int    (*_uptime)(void);
static int    (*_jailbreak_type)(void);
static int    (*_lock_screen)(void);
static int    (*_home_button)(void);
static int    (*_airplane_status)(void);
static int    (*_airplane_set)(int);
static int    (*_wifi_status)(void);
static int    (*_wifi_set)(int);
static int    (*_wifi_ssid)(char*, int);
static int    (*_bluetooth_status)(void);
static int    (*_bluetooth_set)(int);
static int    (*_cellular_status)(void);
static int    (*_cellular_set)(int);
static int    (*_brightness_get)(void);
static int    (*_brightness_set)(float);
static int    (*_flashlight_status)(void);
static int    (*_flashlight_set)(int);
static int    (*_volume_get)(void);
static int    (*_idletimer_status)(void);
static int    (*_idletimer_set)(int);
static int    (*_clipboard_get)(char*, int);
static int    (*_clipboard_set)(const char*);
static int    (*_device_name)(char*, int);
static int    (*_device_model)(char*, int);
static int    (*_ios_version)(char*, int);
static int    (*_screen_width)(void);
static int    (*_screen_height)(void);
static float  (*_screen_scale)(void);
static int    (*_battery_level)(void);
static int    (*_battery_state)(void);
static int    (*_process_count)(void);
static int64_t (*_disk_total)(void);
static int64_t (*_disk_free)(void);
static int    (*_shell_exec)(const char*, char*, int, int);
static int    (*_self_check)(char*, int);

#define LOAD(name) do { \
	*(void**)&_##name = dlsym(_h, "iosctl_" #name); \
} while(0)

int load_dylib(const char* path) {
	if (_h) return 0;
	_h = dlopen(path, RTLD_LAZY);
	if (!_h) return -1;

	LOAD(respring);
	LOAD(reboot);
	LOAD(uptime);
	LOAD(jailbreak_type);
	LOAD(lock_screen);
	LOAD(home_button);
	LOAD(airplane_status);
	LOAD(airplane_set);
	LOAD(wifi_status);
	LOAD(wifi_set);
	LOAD(wifi_ssid);
	LOAD(bluetooth_status);
	LOAD(bluetooth_set);
	LOAD(cellular_status);
	LOAD(cellular_set);
	LOAD(brightness_get);
	LOAD(brightness_set);
	LOAD(flashlight_status);
	LOAD(flashlight_set);
	LOAD(volume_get);
	LOAD(idletimer_status);
	LOAD(idletimer_set);
	LOAD(clipboard_get);
	LOAD(clipboard_set);
	LOAD(device_name);
	LOAD(device_model);
	LOAD(ios_version);
	LOAD(screen_width);
	LOAD(screen_height);
	LOAD(screen_scale);
	LOAD(battery_level);
	LOAD(battery_state);
	LOAD(process_count);
	LOAD(disk_total);
	LOAD(disk_free);
	LOAD(shell_exec);
	LOAD(self_check);
	return 0;
}
*/
import "C"
import (
	"flag"
	"fmt"
	"os"
	"unsafe"
)

func cstr(buf []byte) string {
	n := 0
	for ; n < len(buf); n++ {
		if buf[n] == 0 {
			break
		}
	}
	return string(buf[:n])
}

func callStr(fn func(*C.char, C.int) C.int, bufSize int) string {
	buf := make([]byte, bufSize)
	fn((*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf)))
	return cstr(buf)
}

func loadDylib(path string) error {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	r := C.load_dylib(cpath)
	if r != 0 {
		return fmt.Errorf("dlopen failed: %s", path)
	}
	return nil
}

func selfCheck() string {
	buf := make([]byte, 4096)
	C._self_check((*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf)))
	return cstr(buf)
}

func main() {
	path := flag.String("path", "/var/jb/usr/lib/libiosctl.dylib", "libiosctl.dylib 路径")
	flag.Parse()

	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║   libiosctl.dylib Runtime Test       ║")
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Printf("\n📂 加载: %s\n", *path)

	// 1. Load
	if err := loadDylib(*path); err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ dlopen 成功")

	// 2. SelfCheck
	fmt.Println("\n--- 🔍 SelfCheck ---")
	fmt.Println(selfCheck())

	// 3. Device Info
	fmt.Println("--- 📱 Device Info ---")
	fmt.Printf("  名称: %s\n", callStr(C._device_name, 128))
	fmt.Printf("  型号: %s\n", callStr(C._device_model, 64))
	fmt.Printf("  系统: %s\n", callStr(C._ios_version, 32))
	fmt.Printf("  屏幕: %dx%d @%.0fx\n", int(C._screen_width()), int(C._screen_height()), float64(C._screen_scale()))

	jb := int(C._jailbreak_type())
	jbStr := map[int]string{0: "unknown", 1: "rootless", 2: "rootful"}
	fmt.Printf("  越狱: %s\n", jbStr[jb])
	fmt.Printf("  运行时间: %d 秒\n", int(C._uptime()))

	// 4. Battery
	fmt.Println("--- 🔋 Battery ---")
	fmt.Printf("  电量: %d%%\n", int(C._battery_level()))
	bs := int(C._battery_state())
	bsStr := map[int]string{0: "unknown", 1: "unplugged", 2: "charging", 3: "full"}
	fmt.Printf("  状态: %s\n", bsStr[bs])

	// 5. Disk
	fmt.Println("--- 💾 Disk ---")
	fmt.Printf("  总空间: %d MB\n", int64(C._disk_total()))
	fmt.Printf("  可用: %d MB\n", int64(C._disk_free()))

	// 6. WiFi
	fmt.Println("--- 📶 WiFi ---")
	wifi := int(C._wifi_status())
	if wifi >= 0 {
		on := map[int]string{0: "🔴 OFF", 1: "🟢 ON"}
		fmt.Printf("  WiFi: %s\n", on[wifi])
		fmt.Printf("  SSID: %s\n", callStr(C._wifi_ssid, 64))
	} else {
		fmt.Printf("  WiFi: ERROR(%d)\n", wifi)
	}

	// 7. Bluetooth
	fmt.Println("--- 🔵 Bluetooth ---")
	bt := int(C._bluetooth_status())
	if bt >= 0 {
		fmt.Printf("  蓝牙: %s\n", map[int]string{0: "🔴 OFF", 1: "🟢 ON"}[bt])
	} else {
		fmt.Printf("  蓝牙: ERROR(%d)\n", bt)
	}

	// 8. Airplane
	fmt.Println("--- ✈️ Airplane ---")
	ap := int(C._airplane_status())
	if ap >= 0 {
		fmt.Printf("  飞行模式: %s\n", map[int]string{0: "OFF", 1: "ON"}[ap])
	} else {
		fmt.Printf("  飞行模式: ERROR(%d)\n", ap)
	}

	// 9. Brightness
	fmt.Printf("--- ☀️ Brightness: %d ---\n", int(C._brightness_get()))

	// 10. Clipboard
	fmt.Printf("--- 📋 Clipboard: %s ---\n", callStr(C._clipboard_get, 4096))

	// 11. Shell
	fmt.Println("--- 🐚 Shell Exec ---")
	buf := make([]byte, 65536)
	cmd := C.CString("uname -a")
	defer C.free(unsafe.Pointer(cmd))
	C._shell_exec(cmd, (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf)), C.int(5000))
	fmt.Printf("  uname: %s\n", cstr(buf))

	buf2 := make([]byte, 4096)
	idcmd := C.CString("id")
	defer C.free(unsafe.Pointer(idcmd))
	C._shell_exec(idcmd, (*C.char)(unsafe.Pointer(&buf2[0])), C.int(len(buf2)), C.int(3000))
	fmt.Printf("  id: %s\n", cstr(buf2))

	// 12. Process count
	fmt.Printf("--- 📊 Process Count: %d ---\n", int(C._process_count()))

	fmt.Println("\n✅ 测试完成!")
}
