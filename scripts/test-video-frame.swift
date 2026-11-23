import AppKit
import Foundation

// The WebM fixture is a 96 × 64 red and blue test pattern encoded with VP9.
let helper = URL(fileURLWithPath: CommandLine.arguments[1])
let video = URL(fileURLWithPath: CommandLine.arguments[2])
let output = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".jpg")
defer { try? FileManager.default.removeItem(at: output) }
let process = Process()
process.executableURL = helper
process.arguments = [video.path, output.path]
try process.run()
process.waitUntilExit()
guard process.terminationStatus == 0,
      let jpeg = NSBitmapImageRep(data: try Data(contentsOf: output)),
      jpeg.pixelsWide == 96, jpeg.pixelsHigh == 64,
      let left = jpeg.colorAt(x: 24, y: 32)?.usingColorSpace(.sRGB),
      let right = jpeg.colorAt(x: 72, y: 32)?.usingColorSpace(.sRGB),
      left.redComponent > 0.8, left.blueComponent < 0.2,
      right.blueComponent > 0.8, right.redComponent < 0.2 else {
    fatalError("Expected the original red and blue video frame")
}
