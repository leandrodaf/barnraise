package outfilter

import (
	"path/filepath"
	"strings"
)

// Detect guesses the tool from a shell command.
func Detect(command string) string {
	tool, _ := detect(command)
	return tool
}

// DetectLeading is Detect, and also reports whether the tool is the command
// itself (its first word, after variable assignments) rather than a word
// further on: `go test` is wrapped by the hook, `echo go test` is not.
func DetectLeading(command string) (string, bool) {
	tool, at := detect(command)
	return tool, tool != ToolGeneric && at == firstWord(strings.Fields(command))
}

// firstWord is the index of the first word that is not VAR=value.
func firstWord(f []string) int {
	for i, w := range f {
		if !strings.Contains(w, "=") || strings.HasPrefix(w, "-") || strings.HasPrefix(w, "=") {
			return i
		}
	}
	return len(f)
}

func detect(command string) (string, int) {
	f := strings.Fields(command)
	for i, w := range f {
		base := filepath.Base(strings.Trim(w, `"'`))
		next := ""
		if i+1 < len(f) {
			next = f[i+1]
		}
		rest := f[i+1:]
		switch base {
		case "gradlew", "gradle", "gradlew.bat":
			return ToolGradle, i
		case "go":
			if next == "test" || next == "vet" || next == "build" {
				return ToolGo, i
			}
		case "jest":
			return ToolJest, i
		case "vitest":
			return ToolVitest, i
		case "pytest", "py.test":
			return ToolPytest, i
		case "maestro":
			return ToolMaestro, i
		case "python", "python3":
			if next == "-m" && i+2 < len(f) && f[i+2] == "pytest" {
				return ToolPytest, i
			}
			if next == "-m" && i+3 < len(f) && f[i+2] == "pip" && f[i+3] == "install" {
				return ToolInstall, i
			}
		case "git":
			if t := gitTool(rest); t != "" {
				return t, i
			}
		case "golangci-lint":
			return ToolLint, i
		case "cargo":
			return ToolCargo, i
		case "mvn", "mvnw", "mvnw.cmd":
			return ToolMaven, i
		case "make", "gmake":
			return ToolMake, i
		case "tsc", "vue-tsc":
			return ToolTSC, i
		case "eslint":
			return ToolESLint, i
		case "ls", "tree", "eza", "exa":
			return ToolList, i
		case "find", "fd", "fdfind":
			return ToolFind, i
		case "grep", "egrep", "fgrep", "rg", "ag", "ack":
			return ToolGrep, i
		case "docker", "podman":
			if t := dockerTool(rest); t != "" {
				return t, i
			}
		case "kubectl", "oc":
			if t := kubectlTool(rest); t != "" {
				return t, i
			}
		case "pip", "pip3", "bundle", "poetry", "composer":
			if next == "install" || next == "sync" || next == "update" {
				return ToolInstall, i
			}
		case "uv":
			if next == "sync" || (next == "pip" && i+2 < len(f) && f[i+2] == "install") {
				return ToolInstall, i
			}
		case "npm", "yarn", "pnpm", "bun", "npx":
			if t := jsTool(base, rest); t != "" {
				return t, i
			}
		}
	}
	return ToolGeneric, -1
}

func jsTool(bin string, rest []string) string {
	if len(rest) == 0 {
		if bin == "yarn" {
			return ToolInstall
		}
		return ""
	}
	joined := strings.Join(rest, " ")
	switch rest[0] {
	case "install", "i", "ci", "add":
		return ToolInstall
	}
	switch {
	case strings.Contains(joined, "vitest"):
		return ToolVitest
	case strings.Contains(joined, "jest"):
		return ToolJest
	case strings.Contains(joined, "tsc"):
		return ToolTSC
	case strings.Contains(joined, "eslint"):
		return ToolESLint
	case strings.Contains(joined, "test"):
		return ToolJS
	}
	return ""
}

// subcommand skips the global options of git, docker and kubectl.
func subcommand(rest []string, withArg ...string) (string, []string) {
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		if !strings.HasPrefix(w, "-") {
			return w, rest[i+1:]
		}
		for _, o := range withArg {
			if w == o {
				i++
			}
		}
	}
	return "", nil
}

func gitTool(rest []string) string {
	sub, _ := subcommand(rest, "-C", "-c", "--git-dir", "--work-tree", "--namespace")
	switch sub {
	case "status":
		return ToolGitStatus
	case "diff", "show":
		return ToolGitDiff
	case "log", "reflog", "shortlog":
		return ToolGitLog
	case "push", "pull", "fetch", "clone":
		return ToolGitSync
	case "grep":
		return ToolGrep
	}
	return ""
}

func dockerTool(rest []string) string {
	sub, more := subcommand(rest, "-H", "--host", "--context", "-c", "--config", "--log-level", "-l")
	next := ""
	if len(more) > 0 {
		next = more[0]
	}
	switch sub {
	case "build":
		return ToolDockerBuild
	case "buildx", "builder", "image":
		if next == "build" {
			return ToolDockerBuild
		}
		if next == "ls" || next == "list" {
			return ToolList
		}
	case "compose":
		switch next {
		case "build":
			return ToolDockerBuild
		case "up", "logs":
			return ToolLogs
		case "ps", "images", "ls":
			return ToolList
		}
	case "ps", "images":
		return ToolList
	case "container", "volume", "network":
		if next == "ls" || next == "list" {
			return ToolList
		}
	case "logs":
		return ToolLogs
	}
	return ""
}

func kubectlTool(rest []string) string {
	sub, _ := subcommand(rest, "-n", "--namespace", "--context", "--kubeconfig", "-l", "--selector", "-o", "--output")
	switch sub {
	case "get", "top", "api-resources":
		return ToolList
	case "logs":
		return ToolLogs
	case "describe", "apply", "delete", "rollout", "create", "diff", "wait":
		return ToolKubectl
	}
	return ""
}
