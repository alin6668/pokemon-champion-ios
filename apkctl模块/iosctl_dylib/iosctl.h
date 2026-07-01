/**
 * iosctl.h — iOS 系统级控制 dylib 头文件
 * ==========================================
 * 适用于 Dopamine rootless 越狱 (iOS 15-16)
 * 通过 dlopen + objc runtime 直接调用私有框架
 * 不需要 Activator，不需要 goios 插件
 *
 * 返回值约定:
 *   0  = 成功
 *   -1 = 失败/不可用
 *   正数 = 状态值 (用于 status 类函数)
 */

#ifndef IOSCTL_H
#define IOSCTL_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// ---- Respring / 重启 ----

/** Respring (kill SpringBoard) */
int iosctl_respring(void);

/** 重启设备 */
int iosctl_reboot(void);

/** 获取设备运行时间 (秒) */
int iosctl_uptime(void);

/** 获取越狱类型: 0=不清楚 1=rootless 2=rootful */
int iosctl_jailbreak_type(void);

// ---- 锁屏 / Home 键 ----

/** 锁屏 */
int iosctl_lock_screen(void);

/** Home 键 */
int iosctl_home_button(void);

// ---- 飞行模式 ----

/** 飞行模式状态: 0=关闭 1=开启 */
int iosctl_airplane_status(void);

/** 飞行模式开关 (iOS15+ 可能不支持) */
int iosctl_airplane_set(int on);

// ---- WiFi ----

/** WiFi 开关状态: 0=关闭 1=开启 */
int iosctl_wifi_status(void);

/** WiFi 开关 */
int iosctl_wifi_set(int on);

/** WiFi SSID (buf 需 >= 64) */
int iosctl_wifi_ssid(char *buf, int bufsize);

// ---- 蓝牙 ----

/** 蓝牙状态: 0=关闭 1=开启 */
int iosctl_bluetooth_status(void);

/** 蓝牙开关 */
int iosctl_bluetooth_set(int on);

// ---- 蜂窝数据 ----

/** 蜂窝数据状态: 0=关闭 1=开启 */
int iosctl_cellular_status(void);

/** 蜂窝数据开关 (iOS15+ 可能不支持) */
int iosctl_cellular_set(int on);

// ---- 屏幕亮度 ----

/** 屏幕亮度: 返回 0-100 (百分比) */
int iosctl_brightness_get(void);

/** 屏幕亮度: level 0.0 ~ 1.0 */
int iosctl_brightness_set(float level);

// ---- 手电筒 ----

/** 手电筒状态: 0=关闭 1=开启 */
int iosctl_flashlight_status(void);

/** 手电筒开关 */
int iosctl_flashlight_set(int on);

// ---- 音量 ----

/** 系统音量: 返回 0-100 (百分比) */
int iosctl_volume_get(void);

// ---- 空闲计时器 (防锁屏) ----

/** 获取空闲计时器状态: 0=正常 1=已禁用(屏幕常亮) */
int iosctl_idletimer_status(void);

/** 设置空闲计时器 */
int iosctl_idletimer_set(int disabled);

// ---- 剪贴板 ----

/** 读取剪贴板 (buf 需 >= 4096) */
int iosctl_clipboard_get(char *buf, int bufsize);

/** 写入剪贴板 */
int iosctl_clipboard_set(const char *text);

// ---- 设备信息 ----

/** 设备名称 (buf >= 128) */
int iosctl_device_name(char *buf, int bufsize);

/** 设备型号 (buf >= 64) */
int iosctl_device_model(char *buf, int bufsize);

/** 系统版本 (buf >= 32) */
int iosctl_ios_version(char *buf, int bufsize);

// ---- 屏幕信息 ----

/** 屏幕宽度 (像素) */
int iosctl_screen_width(void);

/** 屏幕高度 (像素) */
int iosctl_screen_height(void);

/** 屏幕缩放 */
float iosctl_screen_scale(void);

// ---- 电池 ----

/** 电池电量: 返回 0-100 */
int iosctl_battery_level(void);

/** 电池状态: 0=未知 1=未充电 2=充电中 3=已充满 */
int iosctl_battery_state(void);

// ---- 进程 ----

/** 进程数 */
int iosctl_process_count(void);

// ---- 磁盘 ----

/** 磁盘总空间 (MB) */
int64_t iosctl_disk_total(void);

/** 磁盘可用空间 (MB) */
int64_t iosctl_disk_free(void);

// ---- Shell 命令 ----

/**
 * 执行 shell 命令并返回输出
 * cmd:        要执行的命令
 * output:     输出缓冲区
 * bufsize:    缓冲区大小
 * timeout_ms: 超时毫秒 (0 = 无超时，建议 10000)
 * 返回: 0=成功, -1=失败
 */
int iosctl_shell_exec(const char *cmd, char *output, int bufsize, int timeout_ms);

// ---- 自检 ----

/** dylib 自检: 检查哪些功能可用，返回可用功能数 */
int iosctl_self_check(char *buf, int bufsize);

#ifdef __cplusplus
}
#endif

#endif /* IOSCTL_H */
