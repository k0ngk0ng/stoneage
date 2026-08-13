import Cocoa
import CoreGraphics
import Foundation
import ScreenCaptureKit

let arguments = Array(CommandLine.arguments.dropFirst())
guard arguments.count == 2 || arguments.count == 3 else {
    FileHandle.standardError.write(Data("usage: capture-window TITLE_SUBSTRING OUTPUT.png [OCCURRENCE]\n".utf8))
    exit(2)
}

let needle = arguments[0].lowercased()
let output = arguments[1]
let occurrence = arguments.count == 3 ? (Int(arguments[2]) ?? -1) : 0
guard occurrence >= 0 else {
    FileHandle.standardError.write(Data("OCCURRENCE must be a non-negative integer\n".utf8))
    exit(2)
}
let options: CGWindowListOption = [.optionOnScreenOnly, .excludeDesktopElements]
guard let windowInfo = CGWindowListCopyWindowInfo(options, kCGNullWindowID) as? [[String: Any]] else {
    FileHandle.standardError.write(Data("unable to list windows\n".utf8))
    exit(1)
}

var matchingNumber: UInt32?
var matchingOwner = ""
var matchingTitle = ""
var matchingOccurrence = 0
for window in windowInfo {
    let owner = (window[kCGWindowOwnerName as String] as? String) ?? ""
    let title = (window[kCGWindowName as String] as? String) ?? ""
    let haystack = "\(owner) \(title)".lowercased()
    guard haystack.contains(needle),
          let number = window[kCGWindowNumber as String] as? UInt32 else { continue }
    if matchingOccurrence != occurrence {
        matchingOccurrence += 1
        continue
    }
    matchingNumber = number
    matchingOwner = owner
    matchingTitle = title
    break
}

if let number = matchingNumber {
    let semaphore = DispatchSemaphore(value: 0)
    var resultCode = 1
    Task {
        do {
            let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
            guard let window = content.windows.first(where: { $0.windowID == number }) else {
                throw NSError(domain: "capture-window", code: 1,
                              userInfo: [NSLocalizedDescriptionKey: "window disappeared before capture"])
            }
            let filter = SCContentFilter(desktopIndependentWindow: window)
            let configuration = SCStreamConfiguration()
            configuration.width = Int(window.frame.width * 2)
            configuration.height = Int(window.frame.height * 2)
            configuration.showsCursor = false
            let image = try await SCScreenshotManager.captureImage(
                contentFilter: filter,
                configuration: configuration
            )
            let bitmap = NSBitmapImageRep(cgImage: image)
            guard let png = bitmap.representation(using: .png, properties: [:]) else {
                throw NSError(domain: "capture-window", code: 2,
                              userInfo: [NSLocalizedDescriptionKey: "could not encode PNG"])
            }
            try png.write(to: URL(fileURLWithPath: output))
            print("owner=\(matchingOwner) title=\(matchingTitle) window=\(number) size=\(image.width)x\(image.height)")
            resultCode = 0
        } catch {
            FileHandle.standardError.write(Data("capture failed: \(error)\n".utf8))
        }
        semaphore.signal()
    }
    semaphore.wait()
    exit(Int32(resultCode))
}

for window in windowInfo {
    let owner = (window[kCGWindowOwnerName as String] as? String) ?? ""
    let title = (window[kCGWindowName as String] as? String) ?? ""
    if !owner.isEmpty || !title.isEmpty { print("candidate owner=\(owner) title=\(title)") }
}
FileHandle.standardError.write(Data("no matching on-screen window\n".utf8))
exit(1)
