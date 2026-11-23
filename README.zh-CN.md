# TG NestFetch

浏览 Telegram 频道附件，试看视频，把选中的文件批量保存到指定目录。

[下载 macOS 版](https://github.com/TNT-Likely/telegram-nestfetch/releases/latest) · [English](README.md)

![频道附件页面，可按日期和类型筛选，并勾选要下载的文件](docs/media/channel-attachments.png)

## 主要功能

- 粘贴 Telegram 消息链接，下载视频、图片、音频或文档。
- 在「频道附件」中选择已加入的频道，或粘贴公开频道链接。按日期和文件类型筛选后，可以勾选多项加入下载队列。
- 在应用内试看视频，查看缓冲进度和 Telegram 取流速度，也可以拖动播放进度条。
- 从「已完成」列表直接播放已保存的视频，本地文件支持离线播放。
- 保存到本地目录或已挂载的网络卷。下载队列支持暂停、重试和更换保存目录。

界面支持简体中文、English，以及日间、暗黑和跟随系统外观。

## 操作演示

![浏览频道附件、试看视频并把选中的文件加入下载队列](docs/media/demo.gif)

演示使用示例频道和 MDN 的视频样片。[样片来源](https://github.com/mdn/interactive-examples/blob/main/live-examples/media/cc0-videos/flower.mp4)

## 安装和登录

1. 下载适用于 macOS 13 或更新版本的 DMG。Apple 芯片选择 `aarch64.dmg`，Intel 芯片选择 `x64.dmg`。[最新版本](https://github.com/TNT-Likely/telegram-nestfetch/releases/latest)
2. 打开 DMG，将 **TG NestFetch** 拖入「应用程序」。应用采用临时签名。如果系统拦截首次启动，可以先尝试打开应用，再到「系统设置 → 隐私与安全性」选择「仍要打开」。[Apple 支持](https://support.apple.com/guide/mac-help/open-a-mac-app-from-an-unidentified-developer-mh40616/mac)
3. 在应用中打开登录二维码，再用手机 Telegram 的「设置 → 设备 → 连接桌面设备」扫码。开启两步验证的账号按提示输入密码。

安装包内置 [tdl](https://github.com/iyear/tdl)，使用独立的 Telegram 会话。当前下载完成或暂停后开始试看，关闭播放器后队列继续。恢复中断的 tdl 下载会重新传输该文件。文件传输完成后如果保存目录离线，则保留在本机暂存区，之后可以重试保存或选择其他目录。

## 平台支持

| 平台 | 当前支持 |
| --- | --- |
| macOS | Apple 芯片和 Intel 安装包，需要 macOS 13 或更新版本 |
| Windows | 已配置 NSIS 构建目标 |
| Linux | 已配置 DEB 和 AppImage 构建目标 |

开发、构建与许可证信息见英文说明。[Build from source](README.md#build-from-source) · [License](README.md#license)
