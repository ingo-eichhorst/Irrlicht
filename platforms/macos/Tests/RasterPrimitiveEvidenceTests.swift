import AppKit
import CryptoKit
import SwiftUI
import XCTest

/// Renders six one-thing-each views through the snapshot rasteriser and
/// publishes their pixels, so #1615's hypothesis can be answered from bytes
/// instead of from which suites happen to pass.
///
/// ## What this is for
///
/// #1615 says, and marks unverified: the renders that differ between a GitHub
/// runner and the reference Mac are the ones drawing GLYPH or VECTOR content,
/// while text over flat fills reproduces. The only evidence for that is which
/// suites fail — `SessionRowSnapshotTests` 24/24, `HistoryViewSnapshotTests`
/// 2/14 — and a `SessionRowView` carries a brand icon, a state icon, three
/// fonts and a gradient at once, so it cannot say which of them moved.
///
/// These six can. Each renders exactly one thing over an opaque flat ground,
/// as a 3 × 2 matrix — three kinds of mark, each painted twice:
///
///   `fill`      a solid rectangle. No antialiasing anywhere. The CONTROL: if
///               this differs, nothing below it is about glyphs.
///   `bezier`    a stroked round-rect. Vector edges, antialiased by
///               CoreGraphics, and NO text — the arm that separates "the AA
///               ramp differs" from "CoreText differs", which the issue's
///               "glyph/vector" phrasing runs together.
///   `text`      one string in the system font. CoreText glyph rasterisation.
///   `sfsymbol`  one SF Symbol. A glyph too, but from a system font file whose
///               VERSION moves with the OS, so it can differ where `text` does
///               not.
///
/// …and then `fill-dynamic`, `text-dynamic`, `sfsymbol-dynamic`: the same
/// three painted with `Color.secondary` rather than an explicit sRGB triple.
/// That column is the second axis, and it is the one that turned out to
/// matter — see its comment at the declaration.
///
/// ## Why it asserts nothing about a reference
///
/// There is deliberately no committed reference PNG and no `assertSnapshot`
/// here, so this suite cannot fail for a rasterisation difference on any host
/// — that is the whole point: it has to run to completion and publish its
/// pixels on BOTH hosts, including the one where the answer is "different".
/// It is therefore not an image-snapshot suite in
/// `ImageSnapshotCIScopeTests`'s sense (nothing here says `as: .pinnedImage`)
/// and is classified there as evidence-only rather than skipped-or-passing.
///
/// What it does assert is that the mechanism ran: six distinct rasters, each
/// with the pixel dimensions the pinned scale implies, each written to disk,
/// and each carrying the KIND of content it claims. A fixture that quietly
/// rendered four blank views would otherwise publish four identical hashes and
/// read as a clean answer.
///
/// ## Where the pixels go
///
/// `$SNAPSHOT_ARTIFACTS/RasterPrimitiveEvidence/` when that is set — the same
/// variable SnapshotTesting 1.19.2 reads for its failure images
/// (`Sources/SnapshotTesting/AssertSnapshot.swift:469`), so one directory
/// collects both — and a temp directory otherwise. The absolute path is
/// printed, because a developer running this locally has no `RUNNER_TEMP` to
/// guess from.
@MainActor
final class RasterPrimitiveEvidenceTests: XCTestCase {

    /// Point size of every primitive. Small on purpose: a per-pixel diff of
    /// 240×80 is something a person can look at whole.
    private static let points = CGSize(width: 120, height: 40)

    /// The appearance every primitive is pinned to.
    ///
    /// Pinned rather than inherited because an unpinned render answers
    /// differently by day and by night, which is exactly the confusion #1509
    /// spent two rounds on (`AGENTS.md`: a real appearance bug was twice read
    /// as toolchain antialiasing). `.darkAqua` matches what the failing
    /// suites pin.
    private static let appearance: NSAppearance.Name = .darkAqua

    private struct Primitive {
        let name: String
        /// How many distinct pixel colours the render must contain. A flat
        /// fill is exactly one; anything with an antialiased edge is many.
        /// This is what stops a blank render passing as evidence.
        let flat: Bool
        let view: () -> AnyView
    }

    private static let primitives: [Primitive] = [
        Primitive(name: "fill", flat: true) {
            AnyView(Rectangle().fill(Color(red: 0.20, green: 0.40, blue: 0.80)))
        },
        Primitive(name: "bezier", flat: false) {
            AnyView(
                ZStack {
                    Color(red: 0.10, green: 0.10, blue: 0.12)
                    RoundedRectangle(cornerRadius: 9)
                        .stroke(Color(red: 0.95, green: 0.95, blue: 0.95), lineWidth: 2)
                        .padding(6)
                }
            )
        },
        Primitive(name: "text", flat: false) {
            AnyView(
                ZStack {
                    Color(red: 0.10, green: 0.10, blue: 0.12)
                    Text("Irrlicht 0123")
                        .font(.system(size: 13))
                        .foregroundColor(Color(red: 0.95, green: 0.95, blue: 0.95))
                }
            )
        },
        Primitive(name: "sfsymbol", flat: false) {
            AnyView(
                ZStack {
                    Color(red: 0.10, green: 0.10, blue: 0.12)
                    Image(systemName: "chevron.right")
                        .font(.system(size: 22))
                        .foregroundColor(Color(red: 0.95, green: 0.95, blue: 0.95))
                }
            )
        },

        // The second column of the matrix: the same three shapes painted with
        // `Color.secondary` instead of an explicit sRGB triple.
        //
        // Added after the first CI run, and the reason is the finding: all
        // three above came back byte-identical between the runner and the
        // reference Mac, while the elements that DO differ in the real suites
        // are a `.secondary`-tinted `questionmark.circle` and a `.secondary`
        // chart label. `Color.secondary` is a dynamic, TRANSLUCENT system
        // colour (label white at ~55% in dark mode), so a difference here and
        // not above separates "glyphs rasterise differently" — which the first
        // column already refutes — from "a translucent dynamic colour resolves
        // or composites differently", which nothing else in the fixture can
        // reach. `fill-dynamic` is the arm that decides whether antialiasing
        // is involved at all: it has no edge anywhere.
        Primitive(name: "fill-dynamic", flat: true) {
            AnyView(
                ZStack {
                    Color(red: 0.10, green: 0.10, blue: 0.12)
                    Rectangle().fill(Color.secondary)
                }
            )
        },
        Primitive(name: "text-dynamic", flat: false) {
            AnyView(
                ZStack {
                    Color(red: 0.10, green: 0.10, blue: 0.12)
                    Text("Irrlicht 0123")
                        .font(.system(size: 13))
                        .foregroundColor(.secondary)
                }
            )
        },
        Primitive(name: "sfsymbol-dynamic", flat: false) {
            AnyView(
                ZStack {
                    Color(red: 0.10, green: 0.10, blue: 0.12)
                    Image(systemName: "chevron.right")
                        .font(.system(size: 22))
                        .foregroundColor(.secondary)
                }
            )
        },
    ]

    /// `$SNAPSHOT_ARTIFACTS/RasterPrimitiveEvidence`, or a temp directory.
    private static func evidenceDirectory() throws -> URL {
        let base = ProcessInfo.processInfo.environment["SNAPSHOT_ARTIFACTS"] ?? NSTemporaryDirectory()
        let dir = URL(fileURLWithPath: base, isDirectory: true)
            .appendingPathComponent("RasterPrimitiveEvidence", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    private func rasterize(_ primitive: Primitive) throws -> NSBitmapImageRep {
        let hosting = NSHostingView(rootView: primitive.view())
        hosting.frame = CGRect(origin: .zero, size: Self.points)
        hosting.appearance = NSAppearance(named: Self.appearance)
        hosting.layoutSubtreeIfNeeded()

        // Through the SAME rasteriser the failing suites use. A fixture that
        // built its own bitmap would be measuring a path CI does not run.
        let image = PinnedScaleSnapshot.rasterize(hosting, scale: PinnedScaleSnapshot.referenceScale)
        guard let rep = image.representations.first as? NSBitmapImageRep else {
            throw XCTSkip("no bitmap representation for \(primitive.name)")
        }
        return rep
    }

    /// The raw device pixels, row by row, with no PNG container around them.
    ///
    /// Hashing the PNG instead would be a different measurement: two hosts can
    /// encode identical pixels into different files (filter choice, zlib
    /// level, chunk order), so a PNG hash mismatch would not be evidence of a
    /// rasterisation difference at all.
    private func pixelBytes(of rep: NSBitmapImageRep) throws -> Data {
        guard let planes = rep.bitmapData else {
            throw XCTSkip("bitmap has no data")
        }
        var out = Data(capacity: rep.pixelsHigh * rep.pixelsWide * rep.samplesPerPixel)
        let rowBytes = rep.pixelsWide * (rep.bitsPerPixel / 8)
        for row in 0..<rep.pixelsHigh {
            out.append(planes.advanced(by: row * rep.bytesPerRow), count: rowBytes)
        }
        return out
    }

    /// How many distinct pixel values the raster contains, capped — the answer
    /// is only ever compared against 1, so counting past a handful is waste.
    private func distinctPixelCount(of rep: NSBitmapImageRep, bytes: Data, cap: Int = 8) -> Int {
        let stride = rep.bitsPerPixel / 8
        guard stride > 0 else { return 0 }
        var seen: Set<[UInt8]> = []
        for offset in Swift.stride(from: 0, to: bytes.count - stride + 1, by: stride) {
            seen.insert(Array(bytes[offset..<(offset + stride)]))
            if seen.count >= cap { return seen.count }
        }
        return seen.count
    }

    /// Renders all four, writes them, and prints one greppable line each.
    ///
    /// One test rather than four so the vacuity guard — all four hashes
    /// distinct — has every raster in hand at once.
    func testPublishesEachPrimitivesPixels() throws {
        let dir = try Self.evidenceDirectory()
        print("raster-primitive dir: \(dir.path)")
        print("raster-primitive host: scale=\(NSScreen.main?.backingScaleFactor ?? -1) "
              + "appearance=\(Self.appearance.rawValue) points=\(Self.points.width)x\(Self.points.height)")

        var digests: [String: String] = [:]
        for primitive in Self.primitives {
            let rep = try rasterize(primitive)

            XCTAssertEqual(rep.pixelsWide, Int(Self.points.width * PinnedScaleSnapshot.referenceScale),
                           "\(primitive.name): pixel width")
            XCTAssertEqual(rep.pixelsHigh, Int(Self.points.height * PinnedScaleSnapshot.referenceScale),
                           "\(primitive.name): pixel height")

            let bytes = try pixelBytes(of: rep)
            XCTAssertGreaterThan(bytes.count, 0, "\(primitive.name): no pixel bytes")

            // The kind check. `fill` must be one flat colour and the other
            // three must not be, so a primitive that silently rendered nothing
            // — the failure that would make this whole fixture a confident
            // wrong answer — is a failure here instead.
            let distinct = distinctPixelCount(of: rep, bytes: bytes)
            if primitive.flat {
                XCTAssertEqual(distinct, 1, "\(primitive.name) is supposed to be a flat fill")
            } else {
                XCTAssertGreaterThan(distinct, 1, "\(primitive.name) rendered a flat area — it drew nothing")
            }

            let digest = SHA256.hash(data: bytes).map { String(format: "%02x", $0) }.joined()
            digests[primitive.name] = digest
            print("raster-primitive \(primitive.name) "
                  + "\(rep.pixelsWide)x\(rep.pixelsHigh) bpp=\(rep.bitsPerPixel) "
                  + "bytes=\(bytes.count) distinct=\(distinct) sha256=\(digest)")

            guard let png = rep.representation(using: .png, properties: [:]) else {
                return XCTFail("\(primitive.name): could not encode PNG")
            }
            let file = dir.appendingPathComponent("\(primitive.name).png")
            try png.write(to: file)
            let written = (try FileManager.default.attributesOfItem(atPath: file.path)[.size] as? Int) ?? 0
            XCTAssertGreaterThan(written, 0, "\(primitive.name): wrote an empty PNG to \(file.path)")

            // The raw planes too. A PNG is what a person looks at; the .raw is
            // what a byte diff on the other host compares, without either side
            // having to agree on an encoder.
            try bytes.write(to: dir.appendingPathComponent("\(primitive.name).raw"))
        }

        XCTAssertEqual(digests.count, Self.primitives.count, "a primitive produced no digest")
        XCTAssertEqual(Set(digests.values).count, Self.primitives.count,
                       "two primitives rasterised to identical bytes — the fixture is not rendering what it claims")

        // How many images the collecting job should find. Written rather than
        // restated in the workflow, because a count typed into a shell script
        // is a number that documents this fixture without being produced by it
        // — the exact drift `AGENTS.md` records for the replay figures. Adding
        // a seventh primitive here moves the workflow's expectation with it.
        let manifest = Self.primitives.map(\.name).joined(separator: "\n") + "\n"
        let manifestURL = dir.appendingPathComponent("manifest.txt")
        try manifest.write(to: manifestURL, atomically: true, encoding: .utf8)
        XCTAssertTrue(FileManager.default.fileExists(atPath: manifestURL.path),
                      "no manifest at \(manifestURL.path)")
    }
}
