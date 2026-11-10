// Package command inspects a Bash script before it runs.
//
// It is an auxiliary analyzer, not a security boundary. It parses the script
// with mvdan.cc/sh and reports effects it can recognize: a command that changes
// state, an indirect execution path, a command name assembled at runtime, or an
// output redirection. The policy engine, the approval flow and the Linux
// runtime remain responsible for containing whatever this package misses.
//
// The analyzer is deliberately conservative. A false positive costs an
// investigation a simpler probe; a false negative would let a script claim to
// be read-only while changing the host. Callers decide what to do with a
// finding; the investigation and executor refuse a script that is declared
// read-only but is not provably read-only.
package command

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// ErrUnsafeScript means the analyzer found an effect that contradicts the way
// the script was declared.
var ErrUnsafeScript = errors.New("script has effects its declaration does not allow")

// Analysis is what the parser could prove about a script.
type Analysis struct {
	// Commands are the static command names in the script.
	Commands []string
	// Writes describe file or host mutations the analyzer recognized.
	Writes []string
	// Indirect describes execution paths where the command is not the one the
	// parser sees, such as eval, bash -c or xargs.
	Indirect []string
	// Dynamic describes command names or sensitive arguments assembled at
	// runtime, which cannot be checked before execution.
	Dynamic []string
	// Network describes commands that talk to the network. It is advisory; no
	// network boundary exists yet.
	Network []string
}

// ReadOnly reports whether the analyzer found no write, indirect execution or
// dynamic command name. It is not a proof: a command the analyzer does not
// recognize may still change state.
func (a Analysis) ReadOnly() bool {
	return len(a.Writes) == 0 && len(a.Indirect) == 0 && len(a.Dynamic) == 0
}

// Summary renders the findings for an error or an audit record.
func (a Analysis) Summary() string {
	var parts []string
	if len(a.Writes) > 0 {
		parts = append(parts, "writes: "+strings.Join(a.Writes, ", "))
	}
	if len(a.Indirect) > 0 {
		parts = append(parts, "indirect execution: "+strings.Join(a.Indirect, ", "))
	}
	if len(a.Dynamic) > 0 {
		parts = append(parts, "dynamic execution: "+strings.Join(a.Dynamic, ", "))
	}
	return strings.Join(parts, "; ")
}

// Err returns a wrapped ErrUnsafeScript when the script is not read-only.
func (a Analysis) Err() error {
	if a.ReadOnly() {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrUnsafeScript, a.Summary())
}

// Analyze parses a Bash script and reports the effects it can recognize.
func Analyze(script string) (Analysis, error) {
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(script), "")
	if err != nil {
		return Analysis{}, fmt.Errorf("parse shell script: %w", err)
	}

	var analysis Analysis
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.Stmt:
			for _, redirect := range n.Redirs {
				analysis.addRedirect(redirect)
			}
		case *syntax.CallExpr:
			analysis.addCall(n)
		}
		return true
	})

	analysis.normalize()
	return analysis, nil
}

func (a *Analysis) addCall(call *syntax.CallExpr) {
	if len(call.Args) == 0 {
		// Assignments alone only change the shell's own environment.
		return
	}

	name, ok := staticWord(call.Args[0])
	if !ok {
		a.addDynamic("the command name is assembled at runtime")
		return
	}
	if name == "" {
		a.addDynamic("the command name is empty")
		return
	}
	a.addCommand(name)

	// A command may be invoked through an absolute or relative path. The
	// basename is what the effect maps are keyed on; keeping the original in
	// Commands makes the audit trail readable.
	base := path.Base(name)

	args := make([]string, 0, len(call.Args)-1)
	dynamicArg := false
	for _, word := range call.Args[1:] {
		if value, ok := staticWord(word); ok {
			args = append(args, value)
		} else {
			dynamicArg = true
			args = append(args, "")
		}
	}

	a.classify(base, args, dynamicArg)
}

func (a *Analysis) classify(name string, args []string, dynamicArg bool) {
	if reason, ok := mutatingCommands[name]; ok {
		a.addWrite(fmt.Sprintf("%s %s", name, reason))
	}
	if reason, ok := indirectCommands[name]; ok {
		a.addIndirect(fmt.Sprintf("%s %s", name, reason))
	}
	if _, ok := networkCommands[name]; ok {
		a.addNetwork(name)
	}

	// Sensitive commands take their meaning from arguments. If an argument is
	// assembled at runtime, the analyzer cannot tell which branch will run.
	if dynamicArg && dynamicArgumentSensitive[name] {
		a.addDynamic(fmt.Sprintf("%s has an argument assembled at runtime", name))
	}

	switch name {
	case "systemctl":
		if sub, ok := firstNonFlag(args); ok {
			if sub == "" {
				a.addDynamic("systemctl subcommand is assembled at runtime")
			} else if !systemctlReadOnly[sub] {
				a.addWrite(fmt.Sprintf("systemctl %s changes unit state", sub))
			}
		}
	case "service":
		if len(args) > 0 && !contains(args, "status") && !contains(args, "--status-all") {
			if contains(args, "") {
				a.addDynamic("service action is assembled at runtime")
			} else {
				a.addWrite("service changes service state")
			}
		}
	case "journalctl":
		for _, arg := range args {
			if journalctlWrites(arg) {
				a.addWrite(fmt.Sprintf("journalctl %s changes journal state", arg))
				break
			}
		}
	case "mount":
		if len(args) > 0 && !mountReadOnly(args) {
			a.addWrite("mount changes mounts")
		}
	case "ip":
		for _, arg := range args {
			if ipWrites[arg] {
				a.addWrite(fmt.Sprintf("ip %s changes network state", arg))
				break
			}
		}
	case "sysctl":
		for _, arg := range args {
			if sysctlWrites(arg) {
				a.addWrite(fmt.Sprintf("sysctl %s changes kernel state", arg))
				break
			}
		}
	case "date":
		for _, arg := range args {
			if arg == "-s" || arg == "--set" {
				a.addWrite("date sets the system clock")
				break
			}
		}
	case "hostname":
		if _, ok := firstNonFlag(args); ok {
			a.addWrite("hostname changes the host name")
		}
	case "hostnamectl", "timedatectl", "localectl":
		for _, arg := range args {
			if strings.HasPrefix(arg, "set-") {
				a.addWrite(fmt.Sprintf("%s %s changes host configuration", name, arg))
				break
			}
		}
	case "loginctl":
		for _, arg := range args {
			if loginctlWrites(arg) {
				a.addWrite(fmt.Sprintf("loginctl %s changes a session", arg))
				break
			}
		}
	case "nmcli":
		if sub, ok := firstNonFlag(args); ok && sub != "" && !nmcliReadOnly[sub] {
			a.addWrite(fmt.Sprintf("nmcli %s changes network configuration", sub))
		}
	case "crontab":
		if !crontabReadOnly(args) {
			a.addWrite("crontab changes scheduled jobs")
		}
	case "find":
		a.classifyFind(args)
	case "awk":
		for _, arg := range args {
			if strings.Contains(strings.ReplaceAll(arg, " ", ""), "system(") {
				a.addIndirect("awk system() runs a command")
				break
			}
		}
	case "sed", "perl":
		for _, arg := range args {
			if arg == "--in-place" || strings.HasPrefix(arg, "--in-place=") || shortFlagHas(arg, 'i') {
				a.addWrite(fmt.Sprintf("%s edits files in place", name))
				break
			}
		}
	case "tar":
		for _, arg := range args {
			if tarWrites(arg) {
				a.addWrite(fmt.Sprintf("tar %s writes an archive or extracts files", arg))
				break
			}
		}
	case "sort":
		for _, arg := range args {
			if strings.HasPrefix(arg, "-o") || arg == "--output" || strings.HasPrefix(arg, "--output=") {
				a.addWrite("sort writes its output to a file")
				break
			}
		}
	case "curl", "wget":
		if downloadTarget, ok := downloadWrites(args); ok {
			a.addWrite(fmt.Sprintf("%s writes %s", name, downloadTarget))
		}
	case "apt", "apt-get", "yum", "dnf", "zypper":
		if !packageQuery(args) {
			a.addWrite(fmt.Sprintf("%s changes installed packages", name))
		}
	case "dpkg":
		if !dpkgQuery(args) {
			a.addWrite("dpkg changes installed packages")
		}
	case "pacman":
		if !pacmanQuery(args) {
			a.addWrite("pacman changes installed packages")
		}
	case "snap":
		if sub, ok := firstNonFlag(args); ok && sub != "" && !snapReadOnly[sub] {
			a.addWrite(fmt.Sprintf("snap %s changes installed packages", sub))
		}
	case "iptables", "ip6tables":
		if !iptablesQuery(args) {
			a.addWrite(fmt.Sprintf("%s changes firewall rules", name))
		}
	case "nft":
		if sub, ok := firstNonFlag(args); ok && sub != "" && sub != "list" {
			a.addWrite(fmt.Sprintf("nft %s changes firewall rules", sub))
		}
	case "ufw":
		if sub, ok := firstNonFlag(args); ok && sub != "" && sub != "status" {
			a.addWrite(fmt.Sprintf("ufw %s changes firewall rules", sub))
		}
	case "firewall-cmd":
		if !firewallQuery(args) {
			a.addWrite("firewall-cmd changes firewall rules")
		}
	case "env":
		if envRunsCommand(args) {
			a.addIndirect("env runs another command")
		}
	case "command":
		if !commandInspects(args) {
			a.addIndirect("command runs another command")
		}
	case "nice", "nohup", "setsid", "timeout", "stdbuf":
		if len(args) > 0 {
			a.addIndirect(fmt.Sprintf("%s runs another command", name))
		}
	}
}

func (a *Analysis) classifyFind(args []string) {
	for _, arg := range args {
		switch {
		case arg == "-delete":
			a.addWrite("find -delete removes files")
			return
		case arg == "-exec" || arg == "-execdir" || arg == "-ok" || arg == "-okdir":
			a.addIndirect(fmt.Sprintf("find %s runs another command", arg))
			return
		case arg == "-fprint" || arg == "-fprint0" || arg == "-fprintf" || arg == "-fls":
			a.addWrite(fmt.Sprintf("find %s writes a file", arg))
			return
		}
	}
}

func (a *Analysis) addRedirect(redirect *syntax.Redirect) {
	if redirect == nil {
		return
	}
	switch redirect.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrInOut, syntax.RdrClob, syntax.AppClob,
		syntax.RdrAll, syntax.RdrAllClob, syntax.AppAll, syntax.AppAllClob:
		target, ok := staticWord(redirect.Word)
		if !ok {
			a.addWrite(fmt.Sprintf("%s redirects to a path assembled at runtime", redirect.Op))
			return
		}
		if safeRedirectTarget(target) {
			return
		}
		a.addWrite(fmt.Sprintf("%s writes %s", redirect.Op, target))
	case syntax.DplOut, syntax.DplIn:
		target, ok := staticWord(redirect.Word)
		if !ok {
			a.addWrite(fmt.Sprintf("%s duplicates into a path assembled at runtime", redirect.Op))
			return
		}
		if target == "" || target == "-" {
			return
		}
		if _, err := strconv.Atoi(target); err == nil || safeRedirectTarget(target) {
			return
		}
		a.addWrite(fmt.Sprintf("%s writes %s", redirect.Op, target))
	}
}

func (a *Analysis) addCommand(name string)    { a.Commands = appendUnique(a.Commands, name) }
func (a *Analysis) addWrite(reason string)    { a.Writes = appendUnique(a.Writes, reason) }
func (a *Analysis) addIndirect(reason string) { a.Indirect = appendUnique(a.Indirect, reason) }
func (a *Analysis) addDynamic(reason string)  { a.Dynamic = appendUnique(a.Dynamic, reason) }
func (a *Analysis) addNetwork(command string) { a.Network = appendUnique(a.Network, command) }

func (a *Analysis) normalize() {
	sort.Strings(a.Commands)
	sort.Strings(a.Writes)
	sort.Strings(a.Indirect)
	sort.Strings(a.Dynamic)
	sort.Strings(a.Network)
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func staticWord(word *syntax.Word) (string, bool) {
	if word == nil {
		return "", false
	}
	var builder strings.Builder
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			builder.WriteString(part.Value)
		case *syntax.SglQuoted:
			builder.WriteString(part.Value)
		case *syntax.DblQuoted:
			for _, inner := range part.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "", false
				}
				builder.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	return builder.String(), true
}

func firstNonFlag(args []string) (string, bool) {
	for _, arg := range args {
		if arg == "" {
			return "", true
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg, true
	}
	return "", false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func journalctlWrites(arg string) bool {
	for _, prefix := range []string{"--rotate", "--vacuum", "--flush", "--sync", "--relinquish", "--setup-keys", "--update-catalog"} {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}

func mountReadOnly(args []string) bool {
	for _, arg := range args {
		if arg == "-l" || arg == "--show-labels" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return false
	}
	return true
}

func loginctlWrites(arg string) bool {
	for _, prefix := range []string{"terminate-", "kill-", "lock-", "unlock-", "flush-", "set-", "enable-", "disable-"} {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}

func crontabReadOnly(args []string) bool {
	if len(args) == 0 {
		return false
	}
	read := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "-l":
			read = true
		case arg == "-u":
			// -u takes the next argument; it does not change the operation.
			index++
		case strings.HasPrefix(arg, "-u"):
			// Attached form, -uUSER.
		case strings.HasPrefix(arg, "-"):
			return false
		default:
			// A file operand installs a new crontab.
			return false
		}
	}
	return read
}

func tarWrites(arg string) bool {
	if strings.HasPrefix(arg, "--") {
		switch arg {
		case "--extract", "--get", "--create", "--append", "--update", "--delete", "--concatenate":
			return true
		}
		return false
	}
	if strings.HasPrefix(arg, "-") {
		for _, flag := range arg[1:] {
			switch flag {
			case 'x', 'c', 'r', 'u', 'A', 'd':
				return true
			}
		}
	}
	return false
}

func downloadWrites(args []string) (string, bool) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "-o" || arg == "--output" || arg == "-O" || arg == "--output-document":
			if index+1 >= len(args) {
				return arg, true
			}
			target := args[index+1]
			index++
			if target == "-" || safeRedirectTarget(target) {
				continue
			}
			return target, true
		case strings.HasPrefix(arg, "--output="):
			target := strings.TrimPrefix(arg, "--output=")
			if target == "-" || safeRedirectTarget(target) {
				continue
			}
			return target, true
		case strings.HasPrefix(arg, "-O"):
			target := strings.TrimPrefix(arg, "-O")
			if target == "" || target == "-" || safeRedirectTarget(target) {
				continue
			}
			return target, true
		}
	}
	return "", false
}

func packageQuery(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "list", "search", "show", "policy", "depends", "rdepends", "info",
			"repolist", "check-update", "repos", "list-updates", "--version", "-v":
			return true
		}
	}
	return len(args) == 0
}

func dpkgQuery(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-l", "--list", "-L", "--listfiles", "-s", "--status", "-p",
			"--print-avail", "-S", "--search", "-c", "--contents", "--version":
			return true
		}
	}
	return len(args) == 0
}

func pacmanQuery(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-Q") || arg == "--query" {
			return true
		}
	}
	return len(args) == 0
}

func iptablesQuery(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-L", "--list", "-S", "--list-rules", "-C", "--check":
			return true
		}
	}
	return len(args) == 0
}

func firewallQuery(args []string) bool {
	for _, arg := range args {
		if arg == "--state" || strings.HasPrefix(arg, "--list-") {
			return true
		}
	}
	return len(args) == 0
}

func envRunsCommand(args []string) bool {
	for _, arg := range args {
		if arg == "" {
			return true
		}
		if strings.HasPrefix(arg, "-") || strings.Contains(arg, "=") {
			continue
		}
		return true
	}
	return false
}

func commandInspects(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "-v", "-V":
		return true
	default:
		return false
	}
}

// safeRedirectTarget is a sink that does not change a file. It keeps the
// common read-only idiom "probe >/dev/null 2>&1" from looking like a write.
func safeRedirectTarget(target string) bool {
	cleaned := path.Clean(target)
	switch cleaned {
	case "/dev/null", "/dev/stdout", "/dev/stderr", "/dev/tty":
		return true
	}
	return strings.HasPrefix(cleaned, "/dev/fd/") || strings.HasPrefix(cleaned, "/proc/self/fd/")
}

var mutatingCommands = map[string]string{
	"rm":        "deletes files",
	"rmdir":     "removes directories",
	"mv":        "moves files",
	"cp":        "copies files",
	"install":   "writes files",
	"mkdir":     "creates directories",
	"touch":     "changes file timestamps",
	"truncate":  "changes file size",
	"ln":        "creates links",
	"chmod":     "changes permissions",
	"chown":     "changes ownership",
	"chgrp":     "changes group ownership",
	"dd":        "writes raw data",
	"tee":       "writes files",
	"shred":     "destroys files",
	"fallocate": "allocates file space",
	"mkfs":      "formats a filesystem",
	"mkswap":    "creates swap space",
	"swapoff":   "disables swap",
	"umount":    "changes mounts",
	"kill":      "signals a process",
	"pkill":     "signals processes",
	"killall":   "signals processes",
	"reboot":    "changes host availability",
	"shutdown":  "changes host availability",
	"poweroff":  "changes host availability",
	"halt":      "changes host availability",
	"useradd":   "creates a user",
	"usermod":   "changes a user",
	"userdel":   "deletes a user",
	"groupadd":  "creates a group",
	"groupmod":  "changes a group",
	"groupdel":  "deletes a group",
	"passwd":    "changes credentials",
	"chpasswd":  "changes credentials",
	"visudo":    "changes sudo policy",
	"logrotate": "rotates logs",
	"docker":    "changes container state",
	"podman":    "changes container state",
	"kubectl":   "changes cluster state",
	"scp":       "copies files to another host",
	"sftp":      "changes remote files",
}

var indirectCommands = map[string]string{
	"eval":        "evaluates code assembled at runtime",
	"source":      "runs another script",
	".":           "runs another script",
	"exec":        "replaces the shell with another process",
	"xargs":       "runs commands from its input",
	"ssh":         "runs a command on another host",
	"systemd-run": "starts another unit",
	"chroot":      "runs a command in another root",
	"nsenter":     "runs a command in another namespace",
	"unshare":     "runs a command in new namespaces",
	"make":        "runs build commands",
	"watch":       "runs a command repeatedly",
	"parallel":    "runs commands in parallel",
	"sudo":        "runs a command with elevated privileges",
	"su":          "runs a shell as another user",
	"doas":        "runs a command with elevated privileges",
	"pkexec":      "runs a command with elevated privileges",
	"bash":        "runs a shell",
	"sh":          "runs a shell",
	"dash":        "runs a shell",
	"zsh":         "runs a shell",
	"ksh":         "runs a shell",
	"fish":        "runs a shell",
	"python":      "runs arbitrary code",
	"python3":     "runs arbitrary code",
	"perl":        "runs arbitrary code",
	"ruby":        "runs arbitrary code",
	"node":        "runs arbitrary code",
	"php":         "runs arbitrary code",
}

var networkCommands = map[string]struct{}{
	"curl": {}, "wget": {}, "nc": {}, "netcat": {}, "socat": {}, "telnet": {},
	"ssh": {}, "scp": {}, "sftp": {},
}

var dynamicArgumentSensitive = map[string]bool{
	"awk": true, "crontab": true, "date": true, "find": true, "hostname": true,
	"hostnamectl": true, "ip": true, "journalctl": true, "localectl": true,
	"loginctl": true, "mount": true, "nmcli": true, "sed": true,
	"sysctl": true, "tar": true, "timedatectl": true,
}

var systemctlReadOnly = map[string]bool{
	"status": true, "show": true, "cat": true, "help": true,
	"list-units": true, "list-unit-files": true, "list-timers": true,
	"list-sockets": true, "list-jobs": true, "list-machines": true,
	"is-active": true, "is-enabled": true, "is-failed": true,
	"is-system-running": true, "get-default": true,
}

var ipWrites = map[string]bool{
	"add": true, "del": true, "delete": true, "set": true, "change": true,
	"replace": true, "flush": true, "append": true, "prepend": true,
	"save": true, "restore": true,
}

var nmcliReadOnly = map[string]bool{
	"show": true, "status": true, "list": true, "get": true, "monitor": true,
	"general": true,
}

var snapReadOnly = map[string]bool{
	"list": true, "info": true, "find": true, "version": true, "services": true,
}

// shortFlagHas reports whether a short option cluster such as -ni contains
// flag. Long options are not treated as clusters.
func shortFlagHas(arg string, flag rune) bool {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") {
		return false
	}
	for _, candidate := range arg[1:] {
		if candidate == flag {
			return true
		}
	}
	return false
}

func sysctlWrites(arg string) bool {
	switch arg {
	case "-w", "--write", "-p", "--load", "--system":
		return true
	}
	return false
}
