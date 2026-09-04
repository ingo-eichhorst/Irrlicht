import DesktopHelperCore
import Foundation

let encoder = JSONEncoder()
encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]

func emit(_ response: HelperResponse) {
    guard let data = try? encoder.encode(response) else {
        FileHandle.standardError.write(Data("claude-desktop-helper: cannot encode JSON response\n".utf8))
        return
    }
    FileHandle.standardOutput.write(data)
    FileHandle.standardOutput.write(Data("\n".utf8))
}

do {
    let input = FileHandle.standardInput.readDataToEndOfFile()
    guard !input.isEmpty else {
        throw HelperFailure(.invalidRequest, "Expected one JSON request on standard input.")
    }
    let request: HelperRequest
    do {
        request = try JSONDecoder().decode(HelperRequest.self, from: input)
    } catch {
        throw HelperFailure(.invalidRequest, "The JSON request is invalid.")
    }
    emit(try CommandRunner.run(request))
} catch let failure as HelperFailure {
    emit(HelperResponse(ok: false, error: ErrorPayload(failure)))
    exit(failure.exitCode)
} catch {
    let failure = HelperFailure(.actionFailed, "The helper failed without a classified result.")
    emit(HelperResponse(ok: false, error: ErrorPayload(failure)))
    exit(failure.exitCode)
}
