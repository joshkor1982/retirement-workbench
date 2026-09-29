package main

// The Advisor runs on the AI subscription you already pay for, through that
// company's own command-line tool. No API keys, no cloud project, no business
// account:
//
//   - Claude:  the `claude` CLI (Claude Code), signed in with Claude Pro or Max.
//   - ChatGPT: the `codex` CLI (OpenAI Codex), signed in with ChatGPT Plus or Pro.
//
// The app hands the CLI one prompt on stdin and reads one reply. The model
// never touches the app directly: it answers in JSON, and the app applies any
// requested changes itself through the same tools the rest of the app uses.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type aiProvider struct {
	Key, Label, Bin, Plan, Install, SignIn string
}

var aiProviders = []aiProvider{
	{Key: "claude", Label: "Claude", Bin: "claude", Plan: "Claude Pro or Max",
		Install: "npm install -g @anthropic-ai/claude-code", SignIn: "claude  (then type /login)"},
	{Key: "chatgpt", Label: "ChatGPT", Bin: "codex", Plan: "ChatGPT Plus or Pro",
		Install: "npm install -g @openai/codex", SignIn: "codex login  (choose Sign in with ChatGPT)"},
}

func findProvider(key string) (aiProvider, bool) {
	for _, p := range aiProviders {
		if p.Key == key {
			return p, true
		}
	}
	return aiProvider{}, false
}

// aiReady reports whether an Advisor provider is chosen.
func (st State) aiReady() bool {
	_, ok := findProvider(st.Settings.AdvisorProvider)
	return ok
}

// migrateAI maps settings from older builds (API key, Vertex AI, "cli") onto
// the subscription providers. Anything Claude-based becomes "claude".
func migrateAI(st *State) {
	switch st.Settings.AdvisorProvider {
	case "claude", "chatgpt", "":
	default: // "cli", "vertex", "anthropic"
		st.Settings.AdvisorProvider = "claude"
	}
}

func providerLabel(key string) string {
	if p, ok := findProvider(key); ok {
		return p.Label
	}
	return key
}

// lookBin finds a CLI on PATH or in the usual per-user install spots, since a
// server started from a launcher may not inherit your shell's PATH.
func lookBin(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	for _, dir := range []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".claude", "local"),
		filepath.Join(home, ".npm-global", "bin"),
		"/opt/homebrew/bin", "/usr/local/bin",
	} {
		if p := filepath.Join(dir, name); fileExists(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("the %s command is not installed", name)
}

// aiStatus is what Settings shows: is the CLI there at all.
func aiStatus(key string) string {
	p, ok := findProvider(key)
	if !ok {
		return ""
	}
	if path, err := lookBin(p.Bin); err == nil {
		return "Found " + path
	}
	return "Not installed yet"
}

// askAI sends one prompt and returns the reply text. readDir, when set, is the
// only folder the model may read (the medical analysis reads your PDFs there);
// otherwise every tool is off and the model can only answer.
func askAI(ctx context.Context, providerKey, prompt, readDir string) (string, error) {
	p, ok := findProvider(providerKey)
	if !ok {
		return "", errors.New("choose Claude or ChatGPT for the Advisor in Settings")
	}
	bin, err := lookBin(p.Bin)
	if err != nil {
		return "", fmt.Errorf("%s. Install it with: %s", err, p.Install)
	}
	work, err := os.MkdirTemp("", "rw-advisor-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)

	var args []string
	outFile := ""
	switch p.Key {
	case "claude":
		args = []string{"-p", "--output-format", "text", "--no-session-persistence"}
		if readDir != "" {
			args = append(args, "--tools", "Read", "--allowedTools", "Read", "--add-dir", readDir)
		} else {
			args = append(args, "--tools", "")
		}
	case "chatgpt":
		outFile = filepath.Join(work, "reply.txt")
		dir := work
		if readDir != "" {
			dir = readDir
		}
		args = []string{"exec", "--skip-git-repo-check", "--sandbox", "read-only", "--color", "never",
			"--cd", dir, "--output-last-message", outFile, "-"}
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = work // an empty folder: nothing of yours for the CLI to wander into
	ownProcessGroup(cmd)
	cmd.Stdin = strings.NewReader(prompt)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			msg = "no answer within 4 minutes"
		}
		return "", fmt.Errorf("%s: %s. If you are not signed in, run: %s", p.Label, firstN(msg, 240), p.SignIn)
	}
	reply := out.String()
	if outFile != "" {
		if b, err := os.ReadFile(outFile); err == nil {
			reply = string(b)
		}
	}
	return strings.TrimSpace(reply), nil
}

// ---------- Advisor actions ----------------------------------------------------

type aiAction struct {
	Tool  string         `json:"tool"`
	Input map[string]any `json:"input"`
}

// toolCatalog renders the Advisor tools as plain text for the prompt.
func toolCatalog() string {
	var b strings.Builder
	for _, t := range advisorTools() {
		fmt.Fprintf(&b, "- %s: %s\n", t["name"], t["description"])
		schema, _ := t["input_schema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		req := map[string]bool{}
		if rs, ok := schema["required"].([]string); ok {
			for _, r := range rs {
				req[r] = true
			}
		}
		names := make([]string, 0, len(props))
		for k := range props {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			pm, _ := props[k].(map[string]any)
			mark := ""
			if req[k] {
				mark = ", required"
			}
			desc := ""
			if d, _ := pm["description"].(string); d != "" {
				desc = ": " + d
			}
			fmt.Fprintf(&b, "    %s (%v%s)%s\n", k, pm["type"], mark, desc)
		}
	}
	return b.String()
}

// parseAdvisorReply reads {"answer": ..., "actions": [...]}. A reply that is
// not JSON is taken as a plain answer with no changes.
func parseAdvisorReply(text string) (string, []aiAction) {
	var r struct {
		Answer  string     `json:"answer"`
		Actions []aiAction `json:"actions"`
	}
	if js := extractJSON(text); js != "" && json.Unmarshal([]byte(js), &r) == nil && (r.Answer != "" || len(r.Actions) > 0) {
		return strings.TrimSpace(r.Answer), r.Actions
	}
	return strings.TrimSpace(text), nil
}
