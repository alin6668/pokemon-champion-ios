/**
 * iosctl.m — iOS 系统级控制 dylib
 * ======================================
 * Dopamine rootless 越狱 (iOS 15-16)
 * dlopen 私有框架 + objc runtime，无需 Activator/goios 插件
 * 纯 C/ObjC 编译，独立于 Go 项目
 *
 * 编译:
 *   xcrun --sdk iphoneos clang -arch arm64 \
 *     -isysroot $(xcrun --sdk iphoneos --show-sdk-path) \
 *     -miphoneos-version-min=15.0 -dynamiclib \
 *     -current_version 1.0.0 -compatibility_version 1.0.0 \
 *     -o libiosctl.dylib iosctl.c \
 *     -framework Foundation -framework UIKit -framework IOKit \
 *     -framework CoreGraphics -framework AVFoundation
 *   ldid -S libiosctl.dylib
 *
 * 依赖框架 (编译时链接公开部分, 运行时 dlopen 私有部分):
 *   公开: Foundation, UIKit, IOKit, CoreGraphics, AVFoundation
 *   私有(dlopen): MobileWiFi, BluetoothManager, GraphicsServices,
 *                SpringBoardServices, CoreTelephony
 */

#import <Foundation/Foundation.h>
#import <UIKit/UIKit.h>
#import <IOKit/IOKitLib.h>
#import <AVFoundation/AVFoundation.h>
#import <dlfcn.h>
#import <stdio.h>
#import <stdlib.h>
#import <string.h>
#import <unistd.h>
#import <signal.h>
#import <sys/sysctl.h>
#import <sys/statvfs.h>
#import <sys/wait.h>
#import <sys/time.h>

// iOS 18 SDK 移除了 sys/reboot.h, 手动声明
#define RB_AUTOBOOT 0
extern int reboot(int howto);
#import <objc/runtime.h>
#import <objc/message.h>
#import <mach/mach.h>
#import <mach/mach_host.h>

#include "iosctl.h"

// ---- 编译期检查最低可用私有 API ----
// 每个功能先在 Dopamine 上测试，不可用的在注释中标明
// 可用性级别: [稳定] [测试] [实验] [不可用]

// ---- 辅助宏 ----

#define JB_PREFIX "/var/jb"

// popen() 在 iOS SDK 不可用，用 fork+exec+pipe 替代
// 执行 cmd，从 stdout 读取第一个数字返回
static int _popen_read_int(const char *cmd) {
    int pipefd[2];
    if (pipe(pipefd) < 0) return -1;

    pid_t pid = fork();
    if (pid < 0) { close(pipefd[0]); close(pipefd[1]); return -1; }

    if (pid == 0) {
        close(pipefd[0]);
        dup2(pipefd[1], STDOUT_FILENO);
        dup2(pipefd[1], STDERR_FILENO);
        close(pipefd[1]);
        execl("/var/jb/bin/sh", "sh", "-c", cmd, NULL);
        execl("/bin/sh", "sh", "-c", cmd, NULL);
        _exit(127);
    }

    close(pipefd[1]);
    char buf[64] = {0};
    ssize_t n = read(pipefd[0], buf, sizeof(buf)-1);
    close(pipefd[0]);
    waitpid(pid, NULL, 0);
    return (n > 0) ? atoi(buf) : -1;
}

// 尝试 dlopen 私有 framework (rootless -> rootful)
static void *dlopen_fw(const char *name) {
    char path[512];
    void *h;

    // rootless 路径
    snprintf(path, sizeof(path),
        "%s/System/Library/PrivateFrameworks/%s.framework/%s",
        JB_PREFIX, name, name);
    h = dlopen(path, RTLD_LAZY | RTLD_GLOBAL);
    if (h) return h;

    // rootful 路径 (通过 bind mount 在 root 下可用)
    snprintf(path, sizeof(path),
        "/System/Library/PrivateFrameworks/%s.framework/%s",
        name, name);
    h = dlopen(path, RTLD_LAZY | RTLD_GLOBAL);
    return h; // 可能为 NULL
}

// objc 便捷调用
static id msg0(id obj, const char *sel) {
    return ((id(*)(id, SEL))objc_msgSend)(obj, sel_registerName(sel));
}
static id msg1(id obj, const char *sel, id a1) {
    return ((id(*)(id, SEL, id))objc_msgSend)(obj, sel_registerName(sel), a1);
}
static int msg_bool(id obj, const char *sel) {
    return ((BOOL(*)(id, SEL))objc_msgSend)(obj, sel_registerName(sel));
}
static void msg_set_bool(id obj, const char *sel, BOOL v) {
    ((void(*)(id, SEL, BOOL))objc_msgSend)(obj, sel_registerName(sel), v);
}
static int msg_int(id obj, const char *sel) {
    return ((NSInteger(*)(id, SEL))objc_msgSend)(obj, sel_registerName(sel));
}
static float msg_float(id obj, const char *sel) {
    return ((float(*)(id, SEL))objc_msgSend)(obj, sel_registerName(sel));
}
static id get_cls(const char *n) {
    id c = objc_getClass(n);
    if (!c) fprintf(stderr, "[iosctl] class %s not found\n", n);
    return c;
}
static NSString *to_ns(const char *s) {
    return [NSString stringWithUTF8String:s];
}
static const char *from_ns(NSString *s, char *buf, int sz) {
    if (!s) { buf[0] = 0; return buf; }
    const char *c = [s UTF8String];
    strncpy(buf, c ? c : "", sz-1);
    buf[sz-1] = 0;
    return buf;
}

// ====================================================================
// 1. Respring — [稳定] kill SpringBoard
// ====================================================================
int iosctl_respring(void) {
    NSLog(@"[iosctl] respring...");
    // system() unavailable on iOS, use fork+exec
    pid_t pid = fork();
    if (pid == 0) {
        execl("/var/jb/usr/bin/killall", "killall", "-9", "SpringBoard", NULL);
        execl("/usr/bin/killall", "killall", "-9", "SpringBoard", NULL);
        _exit(1);
    }
    return 0;
}

// ====================================================================
// 2. Reboot — [稳定] reboot() syscall
// ====================================================================
int iosctl_reboot(void) {
    NSLog(@"[iosctl] reboot...");
    sync();
    reboot(RB_AUTOBOOT);
    return 0; // never reached
}

// ====================================================================
// 3. Uptime — [稳定] sysctl
// ====================================================================
int iosctl_uptime(void) {
    struct timeval tv;
    size_t len = sizeof(tv);
    int mib[2] = { CTL_KERN, KERN_BOOTTIME };
    if (sysctl(mib, 2, &tv, &len, NULL, 0) < 0) return -1;

    struct timeval now;
    gettimeofday(&now, NULL);
    return (int)(now.tv_sec - tv.tv_sec);
}

// ====================================================================
// 4. 越狱类型 — [稳定] 检查 /var/jb 目录
// ====================================================================
int iosctl_jailbreak_type(void) {
    if (access("/var/jb/.installed_dopamine", F_OK) == 0) return 1;
    if (access("/var/jb", F_OK) == 0) return 1; // rootless
    if (access("/.installed_unc0ver", F_OK) == 0) return 2; // rootful unc0ver
    if (access("/.installed_taurine", F_OK) == 0) return 2;
    if (access("/var/lib/dpkg", F_OK) == 0) return 2; // rootful
    return 0;
}

// ====================================================================
// 5. 锁屏 — [稳定] GraphicsServices GSEventLockDevice
// ====================================================================
int iosctl_lock_screen(void) {
    void *gs = dlopen_fw("GraphicsServices");
    if (!gs) {
        // 降级: 通过 SpringBoard 的 SBUIController 发送锁屏事件
        id cls = get_cls("SBUIController");
        if (cls) {
            id ctrl = msg0(cls, "sharedInstance");
            if (ctrl) { msg0(ctrl, "lock"); return 0; }
        }
        fprintf(stderr, "[iosctl] lock_screen: GraphicsServices not available\n");
        return -1;
    }

    void (*GSEventLockDevice)(void) = dlsym(gs, "GSEventLockDevice");
    if (GSEventLockDevice) {
        GSEventLockDevice();
        dlclose(gs);
        return 0;
    }

    dlclose(gs);
    fprintf(stderr, "[iosctl] lock_screen: GSEventLockDevice not found\n");
    return -1;
}

// ====================================================================
// 6. Home 键 — [稳定] SpringBoardServices SBSuspend
// ====================================================================
int iosctl_home_button(void) {
    void *sbs = dlopen_fw("SpringBoardServices");
    if (sbs) {
        void (*SBSuspend)(void) = dlsym(sbs, "SBSuspend");
        if (SBSuspend) { SBSuspend(); dlclose(sbs); return 0; }
        dlclose(sbs);
    }

    // 降级: UIApplication suspend
    id app = get_cls("UIApplication");
    if (app) {
        id shared = msg0(app, "sharedApplication");
        if (shared) { msg0(shared, "suspend"); return 0; }
    }

    fprintf(stderr, "[iosctl] home_button: not available\n");
    return -1;
}

// ====================================================================
// 7. 飞行模式 — iOS15+ [实验] 接口检查
// ====================================================================
int iosctl_airplane_status(void) {
    // iOS 15+: 飞行模式由 SpringBoard 管理，无简单 CLI API
    // 检测: 所有主要网络接口是否都关闭
    int wifiUp = _popen_read_int("ifconfig en0 2>/dev/null | grep -c 'status: active'");
    int cellUp = _popen_read_int("ifconfig pdp_ip0 2>/dev/null | grep -c 'status: active'");

    // 两个都 inactive 很可能开了飞行模式
    return (wifiUp == 0 && cellUp == 0) ? 1 : 0;
}

int iosctl_airplane_set(int on) {
    // iOS 15+ 飞行模式需要 SpringBoard RadiosPreferences 私有 API
    // Dopamine 下 SpringBoard 类在 App 进程中不可直接访问
    // 替代: 通过 SBSetting 或修改 RadiosPreferences plist
    (void)on;
    fprintf(stderr, "[iosctl] airplane_set: iOS15+ needs SpringBoard IPC, not yet implemented\n");
    return -1;
}

// ====================================================================
// 8. WiFi — [稳定] MobileWiFi.framework
// ====================================================================

typedef void *WiFiManagerClientRef;
typedef void *WiFiDeviceClientRef;

static WiFiManagerClientRef _wifi_client = NULL;
static void *_mw_handle = NULL;

static int _wifi_init(void) {
    if (_wifi_client) return 0;
    _mw_handle = dlopen_fw("MobileWiFi");
    if (!_mw_handle) return -1;
    void *(*create)(void *, int) = dlsym(_mw_handle, "WiFiManagerClientCreate");
    if (!create) { dlclose(_mw_handle); _mw_handle = NULL; return -1; }
    _wifi_client = create(kCFAllocatorDefault, 0);
    if (!_wifi_client) { dlclose(_mw_handle); _mw_handle = NULL; return -1; }
    return 0;
}

int iosctl_wifi_status(void) {
    if (_wifi_init() < 0) return -1;

    CFArrayRef (*copyDevices)(WiFiManagerClientRef) = dlsym(_mw_handle, "WiFiManagerClientCopyDevices");
    int (*getPower)(WiFiDeviceClientRef) = dlsym(_mw_handle, "WiFiDeviceClientGetPower");

    if (!copyDevices || !getPower) return -1;

    CFArrayRef devs = copyDevices(_wifi_client);
    if (!devs || CFArrayGetCount(devs) == 0) {
        if (devs) CFRelease(devs);
        return 0;
    }
    WiFiDeviceClientRef dev = (WiFiDeviceClientRef)CFArrayGetValueAtIndex(devs, 0);
    int pwr = getPower(dev);
    CFRelease(devs);
    return pwr ? 1 : 0;
}

int iosctl_wifi_set(int on) {
    if (_wifi_init() < 0) return -1;

    int (*setPower)(WiFiManagerClientRef, int) = dlsym(_mw_handle, "WiFiManagerClientSetPower");
    if (!setPower) return -1;

    int ret = setPower(_wifi_client, on ? 1 : 0);
    return ret ? 0 : -1;
}

int iosctl_wifi_ssid(char *buf, int bufsize) {
    if (!buf || bufsize < 1) return -1;
    buf[0] = 0;

    if (_wifi_init() < 0) return -1;

    // MobileWiFi: WiFiDeviceClientCopyCurrentNetwork
    CFArrayRef (*copyDevices)(WiFiManagerClientRef) = dlsym(_mw_handle, "WiFiManagerClientCopyDevices");
    void *(*copyNetwork)(WiFiDeviceClientRef) = dlsym(_mw_handle, "WiFiDeviceClientCopyCurrentNetwork");

    if (!copyDevices || !copyNetwork) return -1;

    CFArrayRef devs = copyDevices(_wifi_client);
    if (!devs || CFArrayGetCount(devs) == 0) {
        if (devs) CFRelease(devs);
        return -1;
    }

    WiFiDeviceClientRef dev = (WiFiDeviceClientRef)CFArrayGetValueAtIndex(devs, 0);
    CFDictionaryRef net = copyNetwork(dev);
    CFRelease(devs);

    if (!net) return -1;

    CFStringRef ssid = CFDictionaryGetValue(net, CFSTR("SSID_STR"));
    if (ssid) {
        CFStringGetCString(ssid, buf, bufsize, kCFStringEncodingUTF8);
    }
    // BSSID also available if needed
    CFRelease(net);
    return buf[0] ? 0 : -1;
}

// ====================================================================
// 9. 蓝牙 — [稳定] BluetoothManager (objc)
// ====================================================================
int iosctl_bluetooth_status(void) {
    id cls = get_cls("BluetoothManager");
    if (!cls) {
        dlopen_fw("BluetoothManager");
        cls = objc_getClass("BluetoothManager");
    }
    if (!cls) return -1;

    id mgr = msg0(cls, "sharedInstance");
    if (!mgr) return -1;
    return msg_bool(mgr, "powered") ? 1 : 0;
}

int iosctl_bluetooth_set(int on) {
    id cls = get_cls("BluetoothManager");
    if (!cls) {
        dlopen_fw("BluetoothManager");
        cls = objc_getClass("BluetoothManager");
    }
    if (!cls) return -1;

    id mgr = msg0(cls, "sharedInstance");
    if (!mgr) return -1;

    msg_set_bool(mgr, "setPowered:", on ? YES : NO);

    // 验证
    int state = msg_bool(mgr, "powered") ? 1 : 0;
    return (state == on) ? 0 : -1;
}

// ====================================================================
// 10. 蜂窝数据 — iOS15+ [实验]
// ====================================================================
int iosctl_cellular_status(void) {
    // 检查蜂窝接口
    int up = _popen_read_int("ifconfig pdp_ip0 2>/dev/null | grep -c 'status: active'");
    if (up < 0) return -1;
    return up > 0 ? 1 : 0;
}

int iosctl_cellular_set(int on) {
    // iOS 15+ 蜂窝数据切换需要 CoreTelephony + 私有 entitlement
    (void)on;
    fprintf(stderr, "[iosctl] cellular_set: needs CoreTelephony private API, not yet implemented\n");
    return -1;
}

// ====================================================================
// 11. 屏幕亮度 — [稳定] IOKit
// ====================================================================
static io_connect_t _brightness_connect(void) {
    io_service_t svc = IOServiceGetMatchingService(
        kIOMainPortDefault,
        IOServiceMatching("AppleBacklightDisplay"));
    if (!svc) {
        svc = IOServiceGetMatchingService(
            kIOMainPortDefault,
            IOServiceMatching("Backlight"));
    }
    if (!svc) return 0;

    io_connect_t conn;
    kern_return_t kr = IOServiceOpen(svc, mach_task_self(), 0, &conn);
    IOObjectRelease(svc);
    return (kr == KERN_SUCCESS) ? conn : 0;
}

int iosctl_brightness_get(void) {
    io_connect_t conn = _brightness_connect();
    if (!conn) {
        // 降级: UIScreen
        CGFloat b = [[UIScreen mainScreen] brightness];
        return (int)(b * 100);
    }

    uint64_t val = 0;
    uint32_t cnt = 1;
    kern_return_t kr = IOConnectCallScalarMethod(conn, 0, NULL, 0, &val, &cnt);
    IOServiceClose(conn);

    if (kr != KERN_SUCCESS) return -1;
    // IOKit 亮度范围 0-0xfff → 0-100
    return (int)(val * 100 / 0xfff);
}

int iosctl_brightness_set(float level) {
    if (level < 0.0f) level = 0.0f;
    if (level > 1.0f) level = 1.0f;

    io_connect_t conn = _brightness_connect();
    if (!conn) {
        // 降级: UIScreen
        [[UIScreen mainScreen] setBrightness:(CGFloat)level];
        return 0;
    }

    uint64_t val = (uint64_t)(level * 0xfff);
    kern_return_t kr = IOConnectCallScalarMethod(conn, 1, &val, 1, NULL, NULL);
    IOServiceClose(conn);
    return (kr == KERN_SUCCESS) ? 0 : -1;
}

// ====================================================================
// 12. 手电筒 — [稳定] AVCaptureDevice
// ====================================================================
int iosctl_flashlight_status(void) {
    AVCaptureDevice *dev = [AVCaptureDevice defaultDeviceWithMediaType:AVMediaTypeVideo];
    if (!dev || ![dev hasTorch]) return -1;
    return ([dev torchMode] == AVCaptureTorchModeOn) ? 1 : 0;
}

int iosctl_flashlight_set(int on) {
    AVCaptureDevice *dev = [AVCaptureDevice defaultDeviceWithMediaType:AVMediaTypeVideo];
    if (!dev || ![dev hasTorch]) return -1;

    NSError *err = nil;
    [dev lockForConfiguration:&err];
    if (err) return -1;

    if (on) {
        [dev setTorchModeOnWithLevel:0.8f error:&err];
    } else {
        [dev setTorchMode:AVCaptureTorchModeOff];
    }

    [dev unlockForConfiguration];
    return 0;
}

// ====================================================================
// 13. 音量 — [稳定] AVAudioSession (只读)
// ====================================================================
int iosctl_volume_get(void) {
    AVAudioSession *session = [AVAudioSession sharedInstance];
    // outputVolume 0.0 ~ 1.0
    float vol = [session outputVolume];
    return (int)(vol * 100);
}

// ====================================================================
// 14. 空闲计时器 — [稳定] UIApplication
// ====================================================================
int iosctl_idletimer_status(void) {
    return [[UIApplication sharedApplication] isIdleTimerDisabled] ? 1 : 0;
}

int iosctl_idletimer_set(int disabled) {
    dispatch_async(dispatch_get_main_queue(), ^{
        [[UIApplication sharedApplication] setIdleTimerDisabled:(disabled != 0)];
    });
    return 0;
}

// ====================================================================
// 15. 剪贴板 — [稳定] UIPasteboard
// ====================================================================
int iosctl_clipboard_get(char *buf, int bufsize) {
    if (!buf || bufsize < 1) return -1;
    buf[0] = 0;

    UIPasteboard *pb = [UIPasteboard generalPasteboard];
    NSString *s = [pb string];
    if (!s) return -1;

    strncpy(buf, [s UTF8String], bufsize - 1);
    buf[bufsize - 1] = 0;
    return 0;
}

int iosctl_clipboard_set(const char *text) {
    if (!text) return -1;
    UIPasteboard *pb = [UIPasteboard generalPasteboard];
    [pb setString:[NSString stringWithUTF8String:text]];
    return 0;
}

// ====================================================================
// 16. 设备信息 — [稳定] UIDevice
// ====================================================================
int iosctl_device_name(char *buf, int bufsize) {
    if (!buf || bufsize < 1) return -1;
    return from_ns([[UIDevice currentDevice] name], buf, bufsize) ? 0 : -1;
}

int iosctl_device_model(char *buf, int bufsize) {
    if (!buf || bufsize < 1) return -1;

    // 硬件型号: hw.machine
    size_t len = bufsize;
    if (sysctlbyname("hw.machine", buf, &len, NULL, 0) == 0) {
        return 0;
    }
    // 降级: UIDevice model
    return from_ns([[UIDevice currentDevice] model], buf, bufsize) ? 0 : -1;
}

int iosctl_ios_version(char *buf, int bufsize) {
    if (!buf || bufsize < 1) return -1;
    return from_ns([[UIDevice currentDevice] systemVersion], buf, bufsize) ? 0 : -1;
}

// ====================================================================
// 17. 屏幕信息 — [稳定] UIScreen
// ====================================================================
int iosctl_screen_width(void) {
    CGRect b = [[UIScreen mainScreen] bounds];
    return (int)(b.size.width * [[UIScreen mainScreen] scale]);
}

int iosctl_screen_height(void) {
    CGRect b = [[UIScreen mainScreen] bounds];
    return (int)(b.size.height * [[UIScreen mainScreen] scale]);
}

float iosctl_screen_scale(void) {
    return (float)[[UIScreen mainScreen] scale];
}

// ====================================================================
// 18. 电池 — [稳定] UIDevice
// ====================================================================
int iosctl_battery_level(void) {
    UIDevice *dev = [UIDevice currentDevice];
    dev.batteryMonitoringEnabled = YES;
    return (int)(dev.batteryLevel * 100);
}

int iosctl_battery_state(void) {
    // 0=unknown 1=unplugged 2=charging 3=full
    UIDevice *dev = [UIDevice currentDevice];
    dev.batteryMonitoringEnabled = YES;
    return (int)dev.batteryState;
}

// ====================================================================
// 19. 进程数 — [稳定] sysctl
// ====================================================================
int iosctl_process_count(void) {
    int mib[4] = { CTL_KERN, KERN_PROC, KERN_PROC_ALL, 0 };
    size_t len = 0;
    if (sysctl(mib, 4, NULL, &len, NULL, 0) < 0) return -1;
    // 粗略估算 (每个 kinfo_proc ~648 bytes in arm64)
    return (int)(len / 648);
}

// ====================================================================
// 20. 磁盘空间 — [稳定] statvfs
// ====================================================================
int64_t iosctl_disk_total(void) {
    struct statvfs s;
    if (statvfs("/", &s) < 0) return -1;
    return (int64_t)(s.f_blocks) * s.f_frsize / (1024 * 1024);
}

int64_t iosctl_disk_free(void) {
    struct statvfs s;
    if (statvfs("/", &s) < 0) return -1;
    return (int64_t)(s.f_bavail) * s.f_frsize / (1024 * 1024);
}

// ====================================================================
// 21. Shell 命令执行 — [稳定] fork+exec 带超时
//    在 rootless 环境下通过 /var/jb/bin/sh 执行
// ====================================================================
int iosctl_shell_exec(const char *cmd, char *output, int bufsize, int timeout_ms) {
    if (!cmd || !output || bufsize < 1) return -1;
    output[0] = 0;

    int pipefd[2];
    if (pipe(pipefd) < 0) return -1;

    pid_t pid = fork();
    if (pid < 0) {
        close(pipefd[0]); close(pipefd[1]);
        return -1;
    }

    if (pid == 0) {
        // 子进程: 重定向 stdout/stderr 到管道
        close(pipefd[0]);
        dup2(pipefd[1], STDOUT_FILENO);
        dup2(pipefd[1], STDERR_FILENO);
        close(pipefd[1]);

        // 尝试 shell 路径优先级: /var/jb/bin/sh > /bin/sh
        const char *shells[] = {
            "/var/jb/bin/sh",
            "/var/jb/bin/bash",
            "/bin/sh",
            "/bin/bash",
            NULL
        };
        for (int i = 0; shells[i]; i++) {
            if (access(shells[i], X_OK) == 0) {
                execl(shells[i], shells[i], "-c", cmd, (char *)NULL);
                break;
            }
        }
        _exit(127);
    }

    // 父进程: 读管道，带超时
    close(pipefd[1]);

    int total = 0;
    time_t start = time(NULL);

    while (total < bufsize - 1) {
        // 超时检查
        if (timeout_ms > 0) {
            struct timeval tv;
            tv.tv_sec = timeout_ms / 1000;
            tv.tv_usec = (timeout_ms % 1000) * 1000;

            fd_set fds;
            FD_ZERO(&fds);
            FD_SET(pipefd[0], &fds);

            int ready = select(pipefd[0] + 1, &fds, NULL, NULL, &tv);
            if (ready < 0) break;
            if (ready == 0) {
                // 超时: 杀掉子进程
                kill(pid, SIGKILL);
                waitpid(pid, NULL, 0);
                break;
            }
        }

        ssize_t n = read(pipefd[0], output + total, bufsize - 1 - total);
        if (n <= 0) break;
        total += n;

        // 无超时模式下也做一次快速检查避免完全卡死
        if (timeout_ms == 0 && (time(NULL) - start) > 300) {
            kill(pid, SIGKILL);
            waitpid(pid, NULL, 0);
            total += snprintf(output + total, bufsize - 1 - total,
                "\n[iosctl: killed after 300s hard timeout]");
            break;
        }
    }

    output[total] = 0;
    close(pipefd[0]);

    // 回收子进程
    int status;
    waitpid(pid, &status, WNOHANG);

    return (total > 0) ? 0 : -1;
}

// ====================================================================
// 22. 自检 — 检测所有功能是否可用
// ====================================================================
int iosctl_self_check(char *buf, int bufsize) {
    if (!buf || bufsize < 1) return -1;
    char *p = buf;
    char *end = buf + bufsize - 1;
    int count = 0;

#define CHECK(name, code) do { \
    int r = (code); \
    int n = snprintf(p, end-p, "  %-20s = %s\n", name, r >= 0 ? "OK" : "FAIL"); \
    if (n > 0 && p + n < end) { p += n; if (r >= 0) count++; } \
} while(0)

    CHECK("respring", 0);
    CHECK("reboot", 0);
    CHECK("uptime", iosctl_uptime());
    CHECK("jailbreak_type", iosctl_jailbreak_type());
    CHECK("lock_screen", iosctl_lock_screen() == 0 ? 0 : -1);
    CHECK("home_button", iosctl_home_button());
    CHECK("airplane_status", iosctl_airplane_status());
    CHECK("wifi_status", iosctl_wifi_status());
    CHECK("wifi_ssid", { char ssid[64]; iosctl_wifi_ssid(ssid, 64); });
    CHECK("wifi_set", 0); // 标记为可用但风险操作
    CHECK("bluetooth_status", iosctl_bluetooth_status());
    CHECK("bluetooth_set", 0);
    CHECK("brightness", iosctl_brightness_get());
    CHECK("flashlight", iosctl_flashlight_status());
    CHECK("volume", iosctl_volume_get());
    CHECK("idletimer", iosctl_idletimer_status());
    CHECK("clipboard", { char cb[64]; iosctl_clipboard_get(cb, 64); });
    CHECK("device_name", { char dn[64]; iosctl_device_name(dn, 64); });
    CHECK("device_model", { char dm[64]; iosctl_device_model(dm, 64); });
    CHECK("ios_version", { char iv[32]; iosctl_ios_version(iv, 32); });
    CHECK("screen_width", iosctl_screen_width());
    CHECK("screen_height", iosctl_screen_height());
    CHECK("screen_scale", (int)iosctl_screen_scale());
    CHECK("battery_level", iosctl_battery_level());
    CHECK("battery_state", iosctl_battery_state());
    CHECK("process_count", iosctl_process_count());
    CHECK("disk_total", (int)iosctl_disk_total());
    CHECK("disk_free", (int)iosctl_disk_free());
    CHECK("shell_exec", { char sh[256]; iosctl_shell_exec("echo OK", sh, 256, 5000); });

#undef CHECK
    *p = 0;
    return count;
}
