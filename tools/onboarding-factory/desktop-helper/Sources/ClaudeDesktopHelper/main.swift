import DesktopHelperCore
import Foundation

let result = ResponseEncoder.encode(
    RequestProcessor.process(FileHandle.standardInput.readDataToEndOfFile())
)
FileHandle.standardOutput.write(result.data)
if result.exitCode != 0 {
    exit(result.exitCode)
}
