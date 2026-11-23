import AVFoundation
import Foundation
import ImageIO
import UniformTypeIdentifiers

// The sidecar supplies a local video and an output path within its private staging folder.
guard CommandLine.arguments.count == 3 else { exit(64) }

let input = URL(fileURLWithPath: CommandLine.arguments[1])
let output = URL(fileURLWithPath: CommandLine.arguments[2])
let asset = AVURLAsset(url: input)
let generator = AVAssetImageGenerator(asset: asset)
generator.appliesPreferredTrackTransform = true
generator.maximumSize = CGSize(width: 960, height: 540)

let time = CMTime(seconds: 0.35, preferredTimescale: 600)
guard let image = (try? generator.copyCGImage(at: time, actualTime: nil))
    ?? (try? generator.copyCGImage(at: .zero, actualTime: nil)),
    let jpeg = CGImageDestinationCreateWithURL(output as CFURL, UTType.jpeg.identifier as CFString, 1, nil)
else { exit(1) }

let properties = [kCGImageDestinationLossyCompressionQuality: 0.76] as CFDictionary
CGImageDestinationAddImage(jpeg, image, properties)
exit(CGImageDestinationFinalize(jpeg) ? 0 : 1)
