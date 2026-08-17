import CoreGraphics
import ImageIO
import Foundation

guard CommandLine.arguments.count == 3,
      let number = UInt32(CommandLine.arguments[1]) else {
    fputs("usage: capture-window-cg WINDOW_NUMBER OUTPUT.png\n", stderr)
    exit(2)
}
let output = URL(fileURLWithPath: CommandLine.arguments[2])
guard let image = CGWindowListCreateImage(.null, .optionIncludingWindow, number,
                                           [.boundsIgnoreFraming, .bestResolution]) else {
    fputs("could not capture window\n", stderr)
    exit(1)
}
guard let destination = CGImageDestinationCreateWithURL(output as CFURL, "public.png" as CFString, 1, nil) else {
    fputs("could not create png destination\n", stderr)
    exit(1)
}
CGImageDestinationAddImage(destination, image, nil)
guard CGImageDestinationFinalize(destination) else {
    fputs("could not write png\n", stderr)
    exit(1)
}
print("wrote \(output.path) \(image.width)x\(image.height)")
