package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/shimlabs/undo/internal/hook"
	"github.com/shimlabs/undo/internal/terminal"
)

// command is one undo command, with everything its help shows.
type command struct {
	// name is what follows undo, and "" for undo on its own.
	name string
	// arguments describes the positional arguments, such as "<id>".
	arguments string
	// maxArguments is how many positional arguments it takes, or -1 for any.
	maxArguments int
	summary      string
	description  string
	// group is the heading it is listed under, and "" for commands that
	// agents and the watcher run, which help leaves out.
	group    string
	flags    []string
	examples []example
	run      func(args []string, env Env) error
}

type example struct {
	command     string
	description string
}

type flagHelp struct {
	name    string
	short   string
	value   string
	summary string
}

const projectURL = "https://github.com/shimlabs/undo"

var flagHelps = []flagHelp{
	{name: "--yes", short: "-y", summary: "Restore without asking first"},
	{name: "--project", value: "<path>", summary: "Use the project at <path>, even one that was deleted"},
	{name: "--label", value: "<text>", summary: "Name the checkpoint"},
	{name: "--copy", summary: "Keep full copies, on filesystems that cannot clone"},
}

var groups = []string{"Restore", "Checkpoints", "Agents"}

func commands() []command {
	agents := strings.Join(hook.AgentNames(), ", ")
	return []command{
		{
			name:     "",
			summary:  "Restore the project to before the last command",
			group:    "Restore",
			flags:    []string{"--yes", "--project"},
			run:      undoCommand,
			examples: []example{{"undo", "Undo what the agent just did"}, {"undo --yes", "Undo it without the question"}},
			description: "Shows what the last command changed and asks before it restores anything. " +
				"It first takes a checkpoint of the project as it is, so running undo again puts everything back.",
		},
		{
			name: "to", arguments: "<id>", maxArguments: 1,
			summary: "Restore the project to checkpoint <id>",
			group:   "Restore",
			flags:   []string{"--yes", "--project"},
			run:     toCommand,
			examples: []example{
				{"undo to 38", "Go back to checkpoint 38"},
				{"undo to 3 --project ~/code/app", "Bring back a deleted project"},
			},
			description: "Shows what checkpoint <id> would change and asks before it restores anything. " +
				"It first takes a checkpoint of the project as it is, so running undo again puts everything back. " +
				"Find the id with undo list.",
		},
		{
			name: "diff", arguments: "<id>", maxArguments: 1,
			summary:     "Show what restoring checkpoint <id> would change",
			group:       "Restore",
			flags:       []string{"--project"},
			run:         diffCommand,
			examples:    []example{{"undo diff 38", "See what going back to checkpoint 38 changes"}},
			description: "Lists the paths that restoring checkpoint <id> would write back or remove, and changes nothing.",
		},
		{
			name:        "list",
			summary:     "List checkpoints, the newest last",
			group:       "Restore",
			flags:       []string{"--project"},
			run:         listCommand,
			examples:    []example{{"undo list", "See every checkpoint of this project"}},
			description: "Lists each checkpoint with its id, its age, the command that followed it, and the folder and agent it came from.",
		},
		{
			name:        "checkpoint",
			summary:     "Take a checkpoint now",
			group:       "Checkpoints",
			flags:       []string{"--label", "--copy", "--project"},
			run:         checkpointCommand,
			examples:    []example{{`undo checkpoint --label "before the refactor"`, "Mark a point to come back to"}},
			description: "Agents with an installed hook take checkpoints on their own; this takes one by hand.",
		},
		{
			name: "run", arguments: "-- <command>", maxArguments: -1,
			summary:     "Take a checkpoint, then run <command>",
			group:       "Checkpoints",
			run:         runCommand,
			examples:    []example{{"undo run -- ./scripts/migrate.sh", "Protect a script before it runs"}},
			description: "Protects a command from any tool that undo has no hook for. The checkpoint is labeled with the command.",
		},
		{
			name:        "status",
			summary:     "Show what undo protects and how much disk it uses",
			group:       "Checkpoints",
			flags:       []string{"--project"},
			run:         statusCommand,
			examples:    []example{{"undo status", "Check that the watcher and hooks are running"}},
			description: "Shows the store, the checkpoints, the disk they use, the watcher, the installed hooks and what undo leaves out.",
		},
		{
			name: "install", arguments: "<agent>", maxArguments: 1,
			summary:     "Take a checkpoint before every change the agent makes",
			group:       "Agents",
			run:         installCommand,
			examples:    []example{{"undo install claude", "Protect every Claude Code session"}},
			description: "Adds a hook to the agent's settings, so every shell command and file edit it makes is preceded by a checkpoint. Agents: " + agents + ".",
		},
		{
			name: "uninstall", arguments: "<agent>", maxArguments: 1,
			summary:     "Remove the agent's hook",
			group:       "Agents",
			run:         uninstallCommand,
			examples:    []example{{"undo uninstall claude", "Stop taking checkpoints for Claude Code"}},
			description: "Removes the hook undo install added. Checkpoints already taken stay until they expire. Agents: " + agents + ".",
		},
		{name: "version", run: versionCommand},
		{name: "help", arguments: "[command]", maxArguments: 1, run: helpCommand},
		{name: "hook", maxArguments: -1, run: func(args []string, env Env) error { hookCommand(args, env); return nil }},
		{name: "watch", run: func(_ []string, env Env) error { return watchCommand(env) }},
	}
}

func findCommand(name string) (command, bool) {
	for _, cmd := range commands() {
		if cmd.name == name {
			return cmd, true
		}
	}
	return command{}, false
}

func findFlag(name string) (flagHelp, bool) {
	for _, flag := range flagHelps {
		if flag.name == name || flag.short != "" && flag.short == name {
			return flag, true
		}
	}
	return flagHelp{}, false
}

// usageLine returns how cmd is typed, such as "undo to <id> [flags]".
func (cmd command) usageLine() string {
	return strings.Join(slices.DeleteFunc([]string{"undo", cmd.name, cmd.arguments, flagsHint(cmd)}, isEmpty), " ")
}

// listing returns how cmd appears in the list of commands.
func (cmd command) listing() string {
	return strings.Join(slices.DeleteFunc([]string{"undo", cmd.name, cmd.arguments}, isEmpty), " ")
}

func flagsHint(cmd command) string {
	if len(cmd.flags) == 0 {
		return ""
	}
	return "[flags]"
}

func isEmpty(text string) bool { return text == "" }

func helpCommand(args []string, env Env) error {
	out := env.stdout()
	if len(args) == 0 {
		printHelp(out)
		return nil
	}
	cmd, ok := findCommand(args[0])
	if !ok || cmd.group == "" {
		return unknownCommandError(args[0])
	}
	printCommandHelp(out, cmd)
	return nil
}

func versionCommand(_ []string, env Env) error {
	env.stdout().Printf("undo %s\n", Version)
	return nil
}

// printHelp writes the overview of every command.
func printHelp(out *terminal.Writer) {
	all := commands()
	width := 0
	for _, cmd := range all {
		if cmd.group != "" {
			width = max(width, len(cmd.listing()))
		}
	}
	out.Println("undo takes a checkpoint before every command your coding agent runs,")
	out.Println("and puts your project back when one goes wrong.")
	out.Println()
	out.Println(out.Bold("Usage:"))
	out.Printf("  undo %s %s\n", out.Dim("<command>"), out.Dim("[flags]"))
	for _, group := range groups {
		out.Println()
		out.Println(out.Bold(group + ":"))
		for _, cmd := range all {
			if cmd.group == group {
				out.Printf("  %-*s  %s\n", width, cmd.listing(), cmd.summary)
			}
		}
		if group == "Agents" {
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

func flagListing(flag flagHelp) string {
	listing := flag.name
	if flag.short != "" {
		listing = flag.short + ", " + listing
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
