import DesktopHelperCore
import Foundation

struct EncodedProcessResult {
    let data: Data
    let exitCode: Int32
}

enum ResponseEncoder {
    private static let fallback = Data(
        #"{"error":{"code":"action_failed","message":"The helper could not encode its JSON response."},"ok":false,"protocolVersion":1}"#.utf8
    )

    static func encode(_ result: ProcessResult) -> EncodedProcessResult {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        do {
            var data = try encoder.encode(result.response)
            data.append(0x0A)
            return EncodedProcessResult(data: data, exitCode: result.exitCode)
        } catch {
            var data = fallback
            data.append(0x0A)
            return EncodedProcessResult(
                data: data,
                exitCode: HelperFailure(
                    .actionFailed,
                    "The helper could not encode its JSON response."
                ).exitCode
            )
        }
    }
}
