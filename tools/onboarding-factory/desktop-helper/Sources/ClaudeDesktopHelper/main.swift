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

let result = RequestProcessor.process(FileHandle.standardInput.readDataToEndOfFile())
emit(result.response)
if result.exitCode != 0 {
    exit(result.exitCode)
}
