// pdfmerge - combine PDFs and images into one PDF, with an optional cover page.
// usage: swift pdfmerge.swift <out.pdf> [--cover <coverTextFile>] <input>...
import Foundation
import PDFKit
import AppKit
import CoreText

func coverPage(_ text: String) -> PDFPage? {
    let pageRect = CGRect(x: 0, y: 0, width: 612, height: 792) // US Letter
    let data = NSMutableData()
    guard let consumer = CGDataConsumer(data: data as CFMutableData) else { return nil }
    var mediaBox = pageRect
    guard let ctx = CGContext(consumer: consumer, mediaBox: &mediaBox, nil) else { return nil }
    ctx.beginPDFPage(nil)
    let attr = NSMutableAttributedString(string: text)
    attr.addAttribute(.font, value: NSFont.systemFont(ofSize: 11), range: NSRange(location: 0, length: attr.length))
    let textRect = pageRect.insetBy(dx: 54, dy: 54)
    let path = CGPath(rect: textRect, transform: nil)
    let fs = CTFramesetterCreateWithAttributedString(attr as CFAttributedString)
    let frame = CTFramesetterCreateFrame(fs, CFRangeMake(0, 0), path, nil)
    CTFrameDraw(frame, ctx)
    ctx.endPDFPage()
    ctx.closePDF()
    if let doc = PDFDocument(data: data as Data), let p = doc.page(at: 0) { return p }
    return nil
}

let args = CommandLine.arguments
guard args.count >= 3 else { FileHandle.standardError.write("usage: pdfmerge out.pdf [--cover file] input...\n".data(using: .utf8)!); exit(2) }
let out = args[1]
var inputs = Array(args[2...])
let merged = PDFDocument()
if inputs.first == "--cover", inputs.count >= 2 {
    let coverFile = inputs[1]; inputs.removeFirst(2)
    if let text = try? String(contentsOfFile: coverFile, encoding: .utf8), let cp = coverPage(text) {
        merged.insert(cp, at: merged.pageCount)
    }
}
var added = 0
for p in inputs {
    let ext = (p as NSString).pathExtension.lowercased()
    if ext == "pdf" {
        if let d = PDFDocument(url: URL(fileURLWithPath: p)) {
            for i in 0..<d.pageCount { if let pg = d.page(at: i) { merged.insert(pg, at: merged.pageCount); added += 1 } }
        } else { FileHandle.standardError.write("skip unreadable pdf: \(p)\n".data(using: .utf8)!) }
    } else if ["jpg","jpeg","png","gif","tiff","heic","bmp"].contains(ext) {
        if let img = NSImage(contentsOfFile: p), let pg = PDFPage(image: img) { merged.insert(pg, at: merged.pageCount); added += 1 }
        else { FileHandle.standardError.write("skip unreadable image: \(p)\n".data(using: .utf8)!) }
    } else {
        FileHandle.standardError.write("skip unsupported: \(p)\n".data(using: .utf8)!)
    }
}
guard merged.pageCount > 0, merged.write(toFile: out) else { FileHandle.standardError.write("nothing merged\n".data(using: .utf8)!); exit(1) }
print("wrote \(out) pages=\(merged.pageCount)")
