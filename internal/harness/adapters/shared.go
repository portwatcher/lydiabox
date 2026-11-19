package adapters

import (
	"encoding/json"
)

// The helpers below let the ACP bridge (internal/acp) build the same tool
// events as the transcript readers.

// ToolSummary returns a one-line form of a tool input (command, path, …).
func ToolSummary(raw json.RawMessage) string { return toolInput(raw) }

// PrettyInput returns a tool input as indented JSON or text.
func PrettyInput(raw json.RawMessage) string { return prettyInput(raw) }

// EditDiff returns the diff body of an edit tool input, or "".
func EditDiff(name string, raw json.RawMessage) string { return editDiff(name, raw, inputObject(raw)) }

// LineDiff marks the changed lines between oldText and newText.
func LineDiff(oldText, newText string) string { return lineDiff(oldText, newText) }

// InputPath returns the file that a tool input names, or "".
func InputPath(raw json.RawMessage) string {
	if obj := inputObject(raw); obj != nil {
		return firstString(obj, "file_path", "filePath", "path", "notebook_path", "AbsolutePath", "TargetFile")
	}
	return ""
}

// OutputText returns the text of a tool output.
func OutputText(raw json.RawMessage) string { return outputText(raw, 0) }

// Images returns the data: URLs of the images in a tool output.
func Images(raw json.RawMessage) []string { return findImages(raw, nil, 0) }

// PlanStatus normalizes a plan step status.
func PlanStatus(s string) string { return planStatus(s) }

// DSHCommand returns the dsh command line with the permission mode, the
// model, and ~/.dsh/lydiabox.env, as the terminal launch uses it.
func DSHCommand(model string, args ...string) []string { return dshCommand(model, args...) }
