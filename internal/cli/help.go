package cli

import (
	"fmt"
	"strings"

	"github.com/shimlabs/undo/internal/hook"
	"github.com/shimlabs/undo/internal/terminal"
)

func helpCommand(parsed flags, env Env) error {
	out := env.stdout()
	if len(parsed.args) == 0 {
		printHelp(out)
		return nil
	}
	cmd, ok := findCommand(parsed.args[0])
	if !ok || cmd.group == "" {
		return unknownCommandError(parsed.args[0])
	}
	printCommandHelp(out, cmd)
	return nil
}

func versionCommand(_ flags, env Env) error {
	env.stdout().Printf("undo %s\n", Version)
	return nil
}

// printHelp writes the overview of every command.
func printHelp(out *terminal.Writer) {
	all := commands()
	width := 0
	for _, cmd := range all {
		if cmd.group != "" {
			width = max(width, len(cmd.synopsis()))
		}
	}
	out.Println("undo takes a checkpoint before every command your coding agent runs,")
	out.Println("and puts your project back when one goes wrong.")
	out.Println()
	out.Println(out.Bold("Usage:"))
	out.Printf("  undo %s %s\n", out.Dim("<command>"), out.Dim("[flags]"))
	for _, group := range commandGroups() {
		out.Println()
		out.Println(out.Bold(group + ":"))
		for _, cmd := range all {
			if cmd.group == group {
				out.Printf("  %-*s  %s\n", width, cmd.synopsis(), cmd.summary)
			}
		}
		if group == groupAgents {
			out.Printf("  %-*s  %s\n", width, "", out.Dim("Agents: "+strings.Join(hook.AgentNames(), ", ")))
		}
	}
	out.Println()
	out.Println(out.Bold("Flags:"))
	out.Printf("  %-*s  %s\n", width, "-h, --help", "Show help for undo or a command")
	out.Printf("  %-*s  %s\n", width, "--version", "Print the version")
	printExamples(out, []example{
		{"undo install claude", "Protect every Claude Code session"},
		{"undo", "Undo what the agent just did"},
		{"undo diff 38", "See what going back to checkpoint 38 changes"},
	}, width)
	out.Println()
	out.Printf("Run %s for a command's flags and details.\n", out.Bold("undo help <command>"))
	out.Printf("Learn more at %s\n", projectURL)
}

// printCommandHelp writes the help of one command.
func printCommandHelp(out *terminal.Writer, cmd command) {
	out.Println(cmd.summary + ".")
	out.Println()
	out.Println(out.Bold("Usage:"))
	out.Printf("  %s\n", cmd.usageLine())
	out.Println()
	for _, line := range wrap(cmd.description, 76) {
		out.Println(line)
	}
	width := 0
	for _, name := range cmd.flags {
		flag, _ := findFlag(name)
		width = max(width, len(flagListing(flag)))
	}
	width = max(width, len("-h, --help"))
	out.Println()
	out.Println(out.Bold("Flags:"))
	for _, name := range cmd.flags {
		flag, _ := findFlag(name)
		out.Printf("  %-*s  %s\n", width, flagListing(flag), flag.summary)
	}
	out.Printf("  %-*s  %s\n", width, "-h, --help", "Show this help")
	printExamples(out, cmd.examples, 0)
}

func flagListing(flag flagDefinition) string {
	listing := flag.name
	if flag.shorthand != "" {
		listing = flag.shorthand + ", " + listing
	}
	if flag.value != "" {
		listing += " " + flag.value
	}
	return listing
}

// printExamples lists examples with their descriptions aligned at least
// minWidth columns in, so they can line up with a list above.
func printExamples(out *terminal.Writer, examples []example, minWidth int) {
	if len(examples) == 0 {
		return
	}
	width := minWidth
	for _, example := range examples {
		width = max(width, len(example.command))
	}
	out.Println()
	out.Println(out.Bold("Examples:"))
	for _, example := range examples {
		out.Printf("  %-*s  %s\n", width, example.command, out.Dim(example.description))
	}
}

// wrap splits text into lines of at most width bytes, between words.
func wrap(text string, width int) []string {
	var lines []string
	line := ""
	for word := range strings.FieldsSeq(text) {
		if line != "" && len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// unknownCommandError reports name as unknown, suggesting the closest
// command when one is a likely typo.
func unknownCommandError(name string) error {
	message := fmt.Sprintf("unknown command %q", name)
	if suggestion, ok := closestCommand(name); ok {
		message += fmt.Sprintf(". Did you mean undo %s?", suggestion)
	}
	return usageError{message: message}
}

// closestCommand returns the listed command nearest to name, if it is
// within two edits.
func closestCommand(name string) (string, bool) {
	best, bestDistance := "", 3
	for _, cmd := range commands() {
		if cmd.group == "" || cmd.name == "" {
			continue
		}
		if distance := editDistance(name, cmd.name); distance < bestDistance {
			best, bestDistance = cmd.name, distance
		}
	}
	return best, best != ""
}

// editDistance returns the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current := make([]int, len(b)+1)
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous = current
	}
	return previous[len(b)]
}
