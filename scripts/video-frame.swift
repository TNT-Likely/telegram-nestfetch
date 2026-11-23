import AppKit
import AVFoundation
import ImageIO
import UniformTypeIdentifiers
import WebKit

guard CommandLine.arguments.count == 3 else { exit(64) }
let input = URL(fileURLWithPath: CommandLine.arguments[1])
let output = URL(fileURLWithPath: CommandLine.arguments[2])
let maximumSize = CGSize(width: 960, height: 540)

func writeNativeFrame() -> Bool {
    let generator = AVAssetImageGenerator(asset: AVURLAsset(url: input))
    generator.appliesPreferredTrackTransform = true
    generator.maximumSize = maximumSize
    let time = CMTime(seconds: 0.35, preferredTimescale: 600)
    guard let image = (try? generator.copyCGImage(at: time, actualTime: nil))
        ?? (try? generator.copyCGImage(at: .zero, actualTime: nil)),
        let jpeg = CGImageDestinationCreateWithURL(output as CFURL, UTType.jpeg.identifier as CFString, 1, nil)
    else { return false }
    CGImageDestinationAddImage(jpeg, image, [kCGImageDestinationLossyCompressionQuality: 0.76] as CFDictionary)
    return CGImageDestinationFinalize(jpeg)
}

if writeNativeFrame() { exit(0) }

// WebKit can decode container/codec pairs such as WebM/VP9 for which
// AVAssetImageGenerator does not produce a frame. Give its local page one
// file-backed Blob so seeking never requires reading the whole video into JS.
final class WebFrame: NSObject, WKUIDelegate, WKNavigationDelegate, WKScriptMessageHandler {
    func webView(_ webView: WKWebView, runOpenPanelWith parameters: WKOpenPanelParameters,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping ([URL]?) -> Void) {
        completionHandler(frame.isMainFrame ? [input] : nil)
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        webView.evaluateJavaScript("document.querySelector('input').click()") { _, error in
            if error != nil { exit(1) }
        }
    }

    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        guard message.frameInfo.isMainFrame, let encoded = message.body as? String,
              encoded.utf8.count <= 512 * 1024, let bytes = Data(base64Encoded: encoded),
              bytes.starts(with: [0xff, 0xd8, 0xff]) else { exit(1) }
        do {
            try bytes.write(to: output, options: .atomic)
            exit(0)
        } catch { exit(1) }
    }
}

let app = NSApplication.shared
app.setActivationPolicy(.prohibited)
let delegate = WebFrame()
let config = WKWebViewConfiguration()
config.websiteDataStore = .nonPersistent()
config.mediaTypesRequiringUserActionForPlayback = []
config.userContentController.add(delegate, name: "frame")
let web = WKWebView(frame: CGRect(origin: .zero, size: maximumSize), configuration: config)
web.uiDelegate = delegate
web.navigationDelegate = delegate
let window = NSWindow(contentRect: web.frame, styleMask: [.borderless], backing: .buffered, defer: false)
window.contentView = web
web.loadHTMLString("""
<!doctype html><meta http-equiv="Content-Security-Policy" content="default-src 'none'; media-src blob:; script-src 'unsafe-inline'">
<input type="file"><video muted playsinline preload="auto"></video><script>
const video = document.querySelector('video');
const picker = document.querySelector('input');
const finish = data => window.webkit.messageHandlers.frame.postMessage(data);
function capture() {
    try {
        if (video.readyState < 2 || !video.videoWidth || !video.videoHeight) return;
        const scale = Math.min(1, \(Int(maximumSize.width)) / video.videoWidth, \(Int(maximumSize.height)) / video.videoHeight);
        const canvas = document.createElement('canvas');
        canvas.width = Math.max(1, Math.round(video.videoWidth * scale));
        canvas.height = Math.max(1, Math.round(video.videoHeight * scale));
        const context = canvas.getContext('2d');
        context.fillStyle = '#dedbd5';
        context.fillRect(0, 0, canvas.width, canvas.height);
        context.drawImage(video, 0, 0, canvas.width, canvas.height);
        finish(canvas.toDataURL('image/jpeg', 0.76).split(',')[1]);
    } catch { finish(''); }
}
video.onloadeddata = () => {
    const time = Number.isFinite(video.duration) ? Math.min(0.35, video.duration / 2) : 0;
    if (time > 0 && video.seekable.length) {
        video.onseeked = capture;
        video.currentTime = time;
        setTimeout(capture, 1500);
    } else { capture(); }
};
video.onerror = () => finish('');
picker.onchange = () => {
    video.src = URL.createObjectURL(picker.files[0]);
    video.load();
};
</script>
""", baseURL: nil)
DispatchQueue.main.asyncAfter(deadline: .now() + 8) { exit(1) }
app.run()
