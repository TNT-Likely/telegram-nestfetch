# TG NestFetch

粘贴 Telegram 消息链接，把视频保存到你选的文件夹。TG NestFetch 是使用 Telegram API 的独立桌面下载器。

Paste a Telegram message link and save the video to a folder you choose. TG NestFetch is an independent desktop downloader that uses the Telegram API.

## 安装与使用

1. 从 [GitHub Releases](https://github.com/TNT-Likely/telegram-nestfetch/releases) 下载 macOS 13 或更新版本对应的 DMG。Apple 芯片使用 `aarch64`，Intel 芯片使用 `x64`。打开 DMG，将应用拖入「应用程序」。
2. 首次使用时，在 [Telegram API development tools](https://my.telegram.org/apps) 创建 Telegram API 应用，然后填入 API ID 和 API Hash。也可以使用你已有的 Telegram API 应用凭据。两项信息只保存在本机。
3. 在手机 Telegram 中打开「设置 → 设备 → 连接桌面设备」，扫描应用内的二维码。开启两步验证时，在应用内输入 Telegram 密码。
4. 粘贴可访问的视频消息链接并下载。默认保存到「下载」，可选择本地目录或已挂载的网络目录。

当前 macOS 安装包采用临时签名。若 Gatekeeper 在首次启动时阻止打开，可在尝试启动后通过「系统设置 → 隐私与安全性 → 仍要打开」允许这一个应用。 [Apple 支持文档](https://support.apple.com/guide/mac-help/open-a-mac-app-from-an-unidentified-developer-mh40616/mac)

## Install and use

1. Download the macOS 13+ DMG from [Releases](https://github.com/TNT-Likely/telegram-nestfetch/releases). Choose `aarch64` for Apple silicon or `x64` for an Intel Mac. Open the DMG and drag the app to Applications.
2. Open [Telegram API development tools](https://my.telegram.org/apps) and enter your app's API ID and API Hash. You can use an existing Telegram API app. The credentials are stored locally.
3. On your phone, open Telegram → Settings → Devices → Link Desktop Device and scan the code in TG NestFetch. Enter your Telegram password in the app if two-step verification is enabled.
4. Paste an accessible video message link and start the download. The default destination is Downloads. You can choose a local folder or a mounted network folder.

The macOS bundles are ad hoc signed. If Gatekeeper blocks the first launch, try opening the app once, then use System Settings → Privacy & Security → Open Anyway for this app. [Apple support](https://support.apple.com/guide/mac-help/open-a-mac-app-from-an-unidentified-developer-mh40616/mac)

## 功能 / Features

- 独立 Telegram 登录会话，通过二维码登录，支持两步验证。 / Separate Telegram session with QR sign-in and two-step verification.
- 支持公开消息链接和帐号可访问的私有频道消息链接。 / Public message links and private channel links accessible to the signed-in account.
- 下载队列支持暂停、继续、重试和显示文件位置。保存目录离线时，文件会暂留在本机，等待恢复或改存其他目录。 / Pause, resume, retry, and reveal downloads. If a destination is unavailable, the completed transfer stays on the device for another save attempt.
- 优先显示 Telegram 视频缩略图。macOS 可在下载结束后从本地视频截取封面。 / Telegram thumbnails when available. macOS can capture a frame from the finished local video.
- 简体中文、English，日间模式、暗黑模式和跟随系统。 / Simplified Chinese and English, with light, dark, and system appearance.

## 开发 / Development

Prerequisites are Node.js 20.19+ or 22.12+, Go 1.27+, and a stable Rust toolchain. Install the [Tauri system prerequisites](https://v2.tauri.app/start/prerequisites/) for your platform.

```sh
npm ci
npm --prefix ui ci
npm run dev
```

```sh
npm run check
cd engine
go test -mod=readonly ./...
```

Run `npm run build` from the project root to produce a local bundle. Tauri uses native folder selection and file-manager integration on each desktop OS. The Go sidecar handles the local queue and MTProto, and is compiled for the build target automatically.

| Platform | Bundle configuration | Release |
| --- | --- | --- |
| macOS | App bundle and DMG | 0.1.0 |
| Windows | NSIS | Build target reserved |
| Linux | DEB and AppImage | Build targets reserved |

The [gotd/td](https://github.com/gotd/td) MTProto client handles Telegram sign-in and transfers. TG NestFetch keeps a separate local session and accesses the messages targeted by the download queue.

## License

[MIT](LICENSE).
