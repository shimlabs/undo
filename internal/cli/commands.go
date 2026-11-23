package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/shimlabs/undo/internal/hook"
)

// command is one undo command, with everything its help shows.
type command struct {
	// name is what follows undo, and "" for undo on its own.
	name string
	// arguments describes the positional arguments, such as "<id>".
	arguments string
	// maxArguments is how many positional arguments it takes, or -1 for a
	// command to run, which takes everything after it as it is.
	maxArguments int
	summary      string
	description  string
	// group is the heading it is listed under, and "" for commands help does
	// not list.
	group string
	// isInternal marks the commands agents and the watcher run, which get
	// their arguments as they are.
	isInternal bool
	// aliases are other names that run it, such as ls for list.
	aliases  []string
	flags    []string
	examples []example
	run      func(parsed flags, env Env) error
}

type example struct {
	command     string
	description string
}

// flagDefinition is one flag: how it is typed, what help says about it, and
// how it sets the parsed flags.
type flagDefinition struct {
	name      string
	shorthand string
	// value names the value the flag takes, and is "" for a flag on its own.
	value   string
	summary string
	set     func(parsed *flags, value string)
}

// flags holds the parsed command-line flags and the remaining arguments.
type flags struct {
	isConfirmed bool
	isCopyMode  bool
	isAll       bool
	label       string
	project     string
	args        []string
}

const (
	flagYes     = "--yes"
	flagProject = "--project"
	flagLabel   = "--label"
	flagCopy    = "--copy"
	flagAll     = "--all"
	flagHelp    = "--help"
	flagVersion = "--version"
	// helpFlagListing and versionFlagListing are how help lists the flags
	// that every command, or undo itself, takes.
	helpFlagListing    = "-h, " + flagHelp
	versionFlagListing = "-v, " + flagVersion
)

const projectURL = "https://github.com/shimlabs/undo"

func flagDefinitions() []flagDefinition {
	return []flagDefinition{
		{
			name: flagYes, shorthand: "-y", summary: "Restore without asking first",
			set: func(parsed *flags, _ string) { parsed.isConfirmed = true },
		},
		{
			name: flagProject, value: "<path>", summary: "Use the project at <path>, even one that was deleted",
			set: func(parsed *flags, value string) { parsed.project = value },
		},
		{
			name: flagLabel, value: "<text>", summary: "Name the checkpoint",
			set: func(parsed *flags, value string) { parsed.label = value },
		},
		{
			name: flagCopy, summary: "Keep full copies, on filesystems that cannot clone",
			set: func(parsed *flags, _ string) { parsed.isCopyMode = true },
		},
		{
			name: flagAll, summary: "List every checkpoint, not only the newest 20",
			set: func(parsed *flags, _ string) { parsed.isAll = true },
		},
	}
}

const (
	groupRestore     = "Restore"
	groupCheckpoints = "Checkpoints"
	groupAgents      = "Agents"
)

func commandGroups() []string {
	return []string{groupRestore, groupCheckpoints, groupAgents}
}

func commands() []command {
	agents := strings.Join(hook.AgentNames(), ", ")
	return []command{
		{
			name:     "",
			summary:  "Restore the project to before the last command",
			group:    groupRestore,
			flags:    []string{flagYes, flagProject},
			run:      undoCommand,
			examples: []example{{"undo", "Undo what the agent just did"}, {"undo --yes", "Undo it without the question"}},
			description: "Shows what the last command changed and asks before it restores anything. " +
				"It first takes a checkpoint of the project as it is, so running undo again puts everything back.",
		},
		{
			name: "to", arguments: "<id>", maxArguments: 1,
			summary: "Restore the project to checkpoint <id>",
			group:   groupRestore,
			flags:   []string{flagYes, flagProject},
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
			group:       groupRestore,
			flags:       []string{flagProject},
			run:         diffCommand,
			examples:    []example{{"undo diff 38", "See what going back to checkpoint 38 changes"}},
			description: "Lists the paths that restoring checkpoint <id> would write back or remove, and changes nothing.",
		},
		{
			name:        "list",
			aliases:     []string{"ls"},
			summary:     "List checkpoints, the newest last",
			group:       groupRestore,
			flags:       []string{flagAll, flagProject},
			run:         listCommand,
			examples:    []example{{"undo list", "See every checkpoint of this project"}},
			description: "Lists each checkpoint with its id, its age, the command that followed it, and the folder and agent it came from.",
		},
		{
			name:        "checkpoint",
			summary:     "Take a checkpoint now",
			group:       groupCheckpoints,
			flags:       []string{flagLabel, flagCopy, flagProject},
			run:         checkpointCommand,
			examples:    []example{{`undo checkpoint --label "before the refactor"`, "Mark a point to come back to"}},
			description: "Agents with an installed hook take checkpoints on their own; this takes one by hand.",
		},
		{
			name: "run", arguments: "-- <command>", maxArguments: -1,
			summary:     "Take a checkpoint, then run <command>",
			group:       groupCheckpoints,
			run:         runCommand,
			examples:    []example{{"undo run -- ./scripts/migrate.sh", "Protect a script before it runs"}},
			description: "Protects a command from any tool that undo has no hook for. The checkpoint is labeled with the command.",
		},
		{
			name:        "status",
			summary:     "Show what undo protects and how much disk it uses",
			group:       groupCheckpoints,
			flags:       []string{flagProject},
			run:         statusCommand,
			examples:    []example{{"undo status", "Check that the watcher and hooks are running"}},
			description: "Shows the store, the checkpoints, the disk they use, the watcher, the installed hooks and what undo leaves out.",
		},
		{
			name: "install", arguments: "<agent>", maxArguments: 1,
			summary:     "Take a checkpoint before every change the agent makes",
			group:       groupAgents,
			run:         installCommand,
			examples:    []example{{"undo install claude", "Protect every Claude Code session"}},
			description: "Adds a hook to the agent's settings, so every shell command and file edit it makes is preceded by a checkpoint. Agents: " + agents + ".",
		},
		{
			name: "uninstall", arguments: "<agent>", maxArguments: 1,
			summary:     "Remove the agent's hook",
			group:       groupAgents,
			run:         uninstallCommand,
			examples:    []example{{"undo uninstall claude", "Stop taking checkpoints for Claude Code"}},
			description: "Removes the hook undo install added. Checkpoints already taken stay until they expire. Agents: " + agents + ".",
		},
		{
			name: "version", run: versionCommand,
			summary: "Print the version", description: "Prints the version of undo, as undo --version does.",
		},
		{
			name: "help", arguments: "[command]", maxArguments: 1, run: helpCommand,
			summary: "Show help for undo or a command", description: "Lists every command, or shows the flags and examples of one.",
		},
		{name: "hook", isInternal: true, run: hookCommand},
		{name: "watch", isInternal: true, run: watchCommand},
	}
}

func findCommand(name string) (command, bool) {
	for _, cmd := range commands() {
		if cmd.name == name || slices.Contains(cmd.aliases, name) {
			return cmd, true
		}
	}
	return command{}, false
}

func findFlag(name string) (flagDefinition, bool) {
	for _, flag := range flagDefinitions() {
		if flag.name == name || flag.shorthand != "" && flag.shorthand == name {
			return flag, true
		}
	}
	return flagDefinition{}, false
}

// usageLine returns how cmd is typed, such as "undo to <id> [flags]".
func (cmd command) usageLine() string {
	parts := []string{cmd.synopsis()}
	if len(cmd.flags) > 0 {
		parts = append(parts, "[flags]")
	}
	return strings.Join(parts, " ")
}

// synopsis returns how cmd appears in the list of commands, such as
// "undo to <id>".
func (cmd command) synopsis() string {
	return strings.Join(slices.DeleteFunc([]string{"undo", cmd.name, cmd.arguments}, isEmpty), " ")
}

func isEmpty(text string) bool { return text == "" }

func dispatch(args []string, env Env) error {
	name, rest := "", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, rest = args[0], args[1:]
	}
	if name == "" && len(rest) > 0 && (rest[0] == flagVersion || rest[0] == "-v") {
		return versionCommand(flags{}, env)
	}
	cmd, ok := findCommand(name)
	if !ok {
		return unknownCommandError(name)
	}
	parsed, isHelp, err := parseArguments(cmd, rest)
	if err != nil {
		return err
	}
	if isHelp {
		if cmd.name == "" {
			printHelp(env.stdout())
		} else {
			printCommandHelp(env.stdout(), cmd)
		}
		return nil
	}
	err = cmd.run(parsed, env)
	if usageErr, ok := errors.AsType[usageError](err); ok && usageErr.command == nil {
		usageErr.command = &cmd
		return usageErr
	}
	return err
}

// parseArguments parses args for cmd, and reports whether they ask for
// help. It refuses flags cmd does not take and more arguments than it takes.
func parseArguments(cmd command, args []string) (parsed flags, isHelp bool, err error) {
	if cmd.isInternal {
		return flags{args: args}, false, nil
	}
	if cmd.maxArguments < 0 {
		switch {
		case len(args) > 0 && isHelpFlag(args[0]):
			return flags{}, true, nil
		case len(args) > 0 && args[0] == "--":
			args = args[1:]
		case len(args) > 0 && strings.HasPrefix(args[0], "-"):
			return flags{}, false, usageError{message: "undo " + cmd.name + " takes no flags; put the command after --", command: &cmd}
		}
		return flags{args: args}, false, nil
	}
	return cmd.parseFlags(args)
}

// parseFlags parses the flags and arguments of a command that takes a fixed
// number of arguments.
func (cmd command) parseFlags(args []string) (parsed flags, isHelp bool, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if isHelpFlag(arg) {
			return flags{}, true, nil
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			parsed.args = append(parsed.args, arg)
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		flag, err := cmd.flag(name)
		if err != nil {
			return flags{}, false, err
		}
		if flag.value != "" && !hasValue {
			if i+1 == len(args) {
				return flags{}, false, usageError{message: flag.name + " needs a value", command: &cmd}
			}
			i++
			value = args[i]
		}
		flag.set(&parsed, value)
	}
	if len(parsed.args) > cmd.maxArguments {
		return flags{}, false, cmd.tooManyArgumentsError()
	}
	return parsed, false, nil
}

func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == flagHelp
}

// flag returns the definition of the flag name, if cmd takes it.
func (cmd command) flag(name string) (flagDefinition, error) {
	flag, isKnown := findFlag(name)
	if !isKnown {
		return flagDefinition{}, usageError{message: "unknown flag " + name, command: &cmd}
	}
	if !slices.Contains(cmd.flags, flag.name) {
		return flagDefinition{}, usageError{message: fmt.Sprintf("%s does not take %s", cmd.synopsis(), flag.name), command: &cmd}
	}
	return flag, nil
}

func (cmd command) tooManyArgumentsError() error {
	if cmd.maxArguments == 0 {
		return usageError{message: cmd.synopsis() + " takes no arguments", command: &cmd}
	}
	return usageError{message: "too many arguments for " + cmd.synopsis(), command: &cmd}
}
