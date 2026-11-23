# TG NestFetch

浏览 Telegram 频道附件，试看视频，再把选中的文件保存到你选的目录。也可以直接粘贴 Telegram 消息链接下载。

Browse Telegram channel attachments, preview videos, and save selected files to a folder you choose. You can also download directly from a Telegram message link.

## 安装与使用

1. 从 [GitHub Releases](https://github.com/TNT-Likely/telegram-nestfetch/releases) 下载 macOS 13 或更新版本对应的 DMG。Apple 芯片使用 `aarch64`，Intel 芯片使用 `x64`。打开 DMG，将应用拖入「应用程序」。
2. 从 v0.1.2 起，安装包自带 tdl。点「打开登录二维码」，用手机 Telegram 的「设置 → 设备 → 连接桌面设备」扫码即可。开启两步验证时，在应用内输入 Telegram 密码。应用使用独立会话，首次使用无需申请 API ID 和 API Hash。
3. 粘贴你有权限访问的消息链接，或打开「频道附件」，选择频道和日期范围。可以粘贴公开频道链接，也可以从列表选择已加入的频道。
4. 筛选文件类型、勾选附件，再下载到本地目录或已挂载的网络目录。视频支持直接试看。

当前 macOS 安装包采用临时签名。若 Gatekeeper 在首次启动时阻止打开，可在尝试启动后通过「系统设置 → 隐私与安全性 → 仍要打开」允许这一个应用。 [Apple 支持文档](https://support.apple.com/guide/mac-help/open-a-mac-app-from-an-unidentified-developer-mh40616/mac)

## Install and use

1. Download the macOS 13+ DMG from [Releases](https://github.com/TNT-Likely/telegram-nestfetch/releases). Choose `aarch64` for Apple silicon or `x64` for an Intel Mac. Open the DMG and drag the app to Applications.
2. Version 0.1.2 bundles tdl and uses a separate Telegram session. Select **Sign in with QR code** and scan it on your phone from Telegram → Settings → Devices → Link Desktop Device. Enter your Telegram password in the app if two-step verification is enabled. The bundled client handles the application credentials.
3. Paste a message link you can access or open **Channel attachments** and select a channel and date range. Pick a subscribed channel or paste a public channel link.
4. Filter by file type, select attachments, and save them to a local folder or a mounted network folder. You can preview videos before downloading.

The macOS bundles are ad hoc signed. If Gatekeeper blocks the first launch, try opening the app once, then use System Settings → Privacy & Security → Open Anyway for this app. [Apple support](https://support.apple.com/guide/mac-help/open-a-mac-app-from-an-unidentified-developer-mh40616/mac)

## 功能 / Features

- 独立 Telegram 登录会话，通过二维码登录，支持两步验证。 / Separate Telegram session with QR sign-in and two-step verification.
- 支持公开消息链接和帐号可访问的私有频道消息链接。 / Public message links and private channel links accessible to the signed-in account.
- 支持视频、图片、音频和文档。频道附件可按日历日期分页浏览，勾选后批量加入队列。 / Browse channel attachments by date, then select videos, images, audio, and other files to queue together.
- 试看在窗口内按需读取视频片段，并显示缓冲进度和取流速度。当前下载完成或暂停后可开始试看，关闭试看后队列继续。 / Stream videos in the app with buffering progress and transfer speed. A preview waits for the current download to finish or pause. The queue continues when you close the player.
- 已完成的视频可以从下载列表直接播放。播放时读取保存好的文件，支持离线播放。 / Play finished videos from the download list, including offline playback of local files.
- 下载队列支持暂停、继续、重试和显示文件位置。使用 tdl 时，继续任务会从头下载被中断的文件。目录离线时可从本机暂存文件重试保存或改存其他目录。 / Pause, continue, retry, and reveal downloads. With tdl, continuing an interrupted transfer restarts that file. A finished transfer stays on the device until it can be saved to the original or a new destination.
- 优先显示 Telegram 封面。macOS 也能从本地图片和视频（包括 WebM/VP9）生成预览。 / Telegram covers appear when available. On macOS, missing previews can be generated from local photos and videos, including WebM/VP9.
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

Run `npm run build` from the project root to produce a local bundle. The build verifies the upstream tdl v0.20.4 source archive against a pinned SHA-256 and compiles it with patches for QR sign-in, attachment browsing, and local video playback. The app includes the executable, license, upstream source archive, and the corresponding source changes. The Go sidecar manages the queue and runs tdl with its own data directory and Telegram session. [Upstream tdl release](https://github.com/iyear/tdl/releases/tag/v0.20.4)

Existing installations with saved API credentials continue using their gotd session. To build with your own registered Telegram application, set `TG_NESTFETCH_TELEGRAM_APP_FILE` to the path of a private JSON file containing a numeric `apiId` and a string `apiHash`. The build embeds those credentials in the Go sidecar and keeps the input file out of the checkout. [Telegram developer tools](https://my.telegram.org/apps)

| Platform | Bundle configuration | Release |
| --- | --- | --- |
| macOS | DMG | Available |
| Windows | NSIS | Build target reserved |
| Linux | DEB and AppImage | Build targets reserved |

## License

TG NestFetch is distributed under the MIT License. The bundled tdl utility is distributed under the GNU Affero General Public License v3. [TG NestFetch license](LICENSE) · [tdl license and attribution](src-tauri/resources/tdl/NOTICE)
