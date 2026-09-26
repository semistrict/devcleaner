// Source for the monochrome menu-bar template asset. Regenerate with generate-icons.sh.
import AppKit

let output = CommandLine.arguments[1]
guard let symbol = NSImage(systemSymbolName: "externaldrive.badge.minus", accessibilityDescription: nil),
      let image = symbol.withSymbolConfiguration(NSImage.SymbolConfiguration(pointSize: 17, weight: .regular)),
      let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 96, pixelsHigh: 96,
          bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
          colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0),
      let context = NSGraphicsContext(bitmapImageRep: bitmap) else {
    fatalError("Could not render the menu-bar icon")
}
NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = context
NSAppearance(named: .aqua)!.performAsCurrentDrawingAppearance {
    let scale = min(76 / image.size.width, 76 / image.size.height)
    let size = NSSize(width: image.size.width * scale, height: image.size.height * scale)
    image.draw(in: NSRect(x: (96 - size.width) / 2, y: (96 - size.height) / 2, width: size.width, height: size.height))
}
NSGraphicsContext.restoreGraphicsState()
guard let png = bitmap.representation(using: .png, properties: [:]) else {
    fatalError("Could not encode the menu-bar icon")
}
try png.write(to: URL(fileURLWithPath: output))
