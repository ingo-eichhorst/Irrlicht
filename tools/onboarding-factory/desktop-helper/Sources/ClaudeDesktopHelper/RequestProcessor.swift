import DesktopHelperCore
import Foundation

struct ProcessResult {
    let response: HelperResponse
    let exitCode: Int32
}

enum RequestProcessor {
    static func process(
        _ input: Data,
        run: (HelperRequest) throws -> HelperResponse = { try CommandRunner.run($0) }
    ) -> ProcessResult {
        do {
            guard !input.isEmpty else {
                throw HelperFailure(.invalidRequest, "Expected one JSON request on standard input.")
            }
            let request = try StrictRequestDecoder.decode(input)
            return ProcessResult(response: try run(request), exitCode: 0)
        } catch let failure as HelperFailure {
            return ProcessResult(
                response: HelperResponse(ok: false, error: ErrorPayload(failure)),
                exitCode: failure.exitCode
            )
        } catch {
            let failure = HelperFailure(.actionFailed, "The helper failed without a classified result.")
            return ProcessResult(
                response: HelperResponse(ok: false, error: ErrorPayload(failure)),
                exitCode: failure.exitCode
            )
        }
    }
}
