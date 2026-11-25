import Foundation
import Vision
import AppKit

struct OcrBox {
    let text: String
    let x: Double
    let y: Double // top-down normalized [0..1]
    let h: Double
}

guard CommandLine.arguments.count >= 2 else {
    fputs("Usage: vision-ocr <image_path>\n", stderr)
    exit(1)
}

let imgPath = CommandLine.arguments[1]
guard let nsImg = NSImage(contentsOfFile: imgPath),
      let tiff = nsImg.tiffRepresentation,
      let rep = NSBitmapImageRep(data: tiff),
      let cgImg = rep.cgImage else {
    fputs("Failed to load image: \(imgPath)\n", stderr)
    exit(1)
}

let req = VNRecognizeTextRequest()
req.recognitionLevel = .accurate
req.recognitionLanguages = ["zh-Hans", "en-US"]
req.usesLanguageCorrection = false

let handler = VNImageRequestHandler(cgImage: cgImg, options: [:])
do {
    try handler.perform([req])
} catch {
    fputs("Vision OCR failed: \(error)\n", stderr)
    exit(1)
}

var boxes: [OcrBox] = []
if let results = req.results {
    for obs in results {
        guard let top = obs.topCandidates(1).first else { continue }
        let txt = top.string.trimmingCharacters(in: .whitespacesAndNewlines)
        if txt.isEmpty { continue }
        let bb = obs.boundingBox
        // Vision Y is bottom-up; invert to top-down center Y
        let topY = 1.0 - (bb.origin.y + bb.size.height * 0.5)
        boxes.append(OcrBox(text: txt, x: Double(bb.origin.x), y: Double(topY), h: Double(bb.size.height)))
    }
}

// Sort top-to-bottom, then cluster into rows
boxes.sort { $0.y < $1.y }
var rows: [[OcrBox]] = []
for b in boxes {
    if var lastRow = rows.last, let first = lastRow.first {
        let tol = max(0.012, min(first.h, b.h) * 0.65)
        if abs(b.y - first.y) <= tol {
            lastRow.append(b)
            rows[rows.count - 1] = lastRow
            continue
        }
    }
    rows.append([b])
}

var outRows: [[String]] = []
for row in rows {
    let sorted = row.sorted { $0.x < $1.x }
    outRows.append(sorted.map { $0.text })
}

let payload: [String: Any] = [
    "ok": true,
    "row_count": outRows.count,
    "rows": outRows
]
if let data = try? JSONSerialization.data(withJSONObject: payload, options: [.prettyPrinted]),
   let str = String(data: data, encoding: .utf8) {
    print(str)
}
