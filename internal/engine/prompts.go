package engine

import (
	"fmt"
	"strings"
)

// promptVersion is part of every cache key; bump it when prompts change.
const promptVersion = "2"

const renameSystem = `You are an expert Roblox Luau reverse engineer. You will receive Luau source code produced by a decompiler, with line numbers. Decompilers replace local variable names with meaningless placeholders such as v1, u2, p3 or l_Players_0.

Your job is to recover meaningful names. For each identifier in CANDIDATES, infer what the variable holds from how it is created and used (Roblox services and APIs, instance names passed to WaitForChild/FindFirstChild, connected events, arithmetic, string contents) and propose a descriptive name.

Reply with a single JSON object and nothing else:
{"summary": "...", "renames": {"oldName": "newName"}, "comments": [{"line": 12, "text": "..."}]}

Rules:
- summary: one or two plain sentences describing what the script does.
- renames: only identifiers listed in CANDIDATES. Leave out names that are already descriptive and names you cannot infer with reasonable confidence. Never rename globals, Roblox APIs, table fields or methods.
- New names must be valid Luau identifiers and never Luau keywords: camelCase for values and functions, PascalCase for services, modules and classes. Use distinct names for distinct variables; repeating a name is fine only for variables in unrelated functions.
- comments: %s`

var commentRules = map[string][2]string{
	// [rename-mode rule, rewrite-mode rule]
	"none": {`always an empty list.`,
		`Do not add comments.`},
	"light": {`a few short comments (roughly one per 15-30 lines of code) that explain intent: what a function or block does and why. Never restate the code. "line" is the line number the comment goes above.`,
		`Add a few short comments that explain intent (what a function or block does and why). Never restate the code.`},
	"detailed": {`a comment above every function and every non-trivial block that explains its purpose, inputs and effects. Never restate the code line by line. "line" is the line number the comment goes above.`,
		`Add a comment above every function and every non-trivial block explaining its purpose, inputs and effects. Never restate the code line by line.`},
}

const rewriteSystem = `You are an expert Roblox Luau reverse engineer. You will receive Luau source code produced by a decompiler. Rewrite it so that a human can read and maintain it:
- Rename local variables, upvalues, parameters and local functions to descriptive names inferred from how they are used (camelCase for values and functions, PascalCase for services, modules and classes).
- Clean up decompiler artifacts (redundant temporaries, needlessly nested or inverted conditions, empty branches) only when the behavior stays exactly the same.
- Keep every Roblox API call, string, number, table key, global and side effect exactly as it is. Never add features, remove code, or guess at code that is missing.
- %s
- If the code is only part of a larger script, rewrite just this part and keep the names of variables that are defined elsewhere unchanged.
Reply with only the complete Luau code: no markdown fences and no explanations before or after it.`

func renamePrompt(comments string) string {
	return fmt.Sprintf(renameSystem, commentRules[comments][0])
}
func rewritePrompt(comments string) string {
	return fmt.Sprintf(rewriteSystem, commentRules[comments][1])
}

// numbered renders lines[from-1:to] with right-aligned line numbers.
func numbered(lines []string, from, to int) string {
	width := len(fmt.Sprint(to))
	var sb strings.Builder
	for n := from; n <= to; n++ {
		fmt.Fprintf(&sb, "%*d| %s\n", width, n, strings.TrimRight(lines[n-1], "\r"))
	}
	return sb.String()
}

func renameUserPrompt(candidates []string, code string) string {
	c := strings.Join(candidates, ", ")
	if c == "" {
		c = "(none)"
	}
	return "CANDIDATES: " + c + "\nSOURCE:\n" + code
}

func rewriteUserPrompt(code string) string { return "SOURCE:\n" + code }
