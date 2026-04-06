package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"testing"
)

// --- parseArgs tests ---

func TestParseArgs_HelpLong(t *testing.T) {
	verbose, version, showHelp, printEnv, noOtel, _, _, upstream, logFile, profile, otel, _, _, _, _, _, codexArgs := parseArgs([]string{"--help"})
	if !showHelp {
		t.Error("expected showHelp=true for --help")
	}
	if verbose || version || printEnv || noOtel || otel || upstream != "" || logFile != "" || profile != "" || len(codexArgs) != 0 {
		t.Error("unexpected non-default values alongside --help")
	}
}

func TestParseArgs_HelpShort(t *testing.T) {
	_, _, showHelp, _, _, _, _, _, _, _, _, _, _, _, _, _, _ := parseArgs([]string{"-h"})
	if !showHelp {
		t.Error("expected showHelp=true for -h")
	}
}

func TestParseArgs_PrintEnv(t *testing.T) {
	_, _, _, printEnv, _, _, _, _, _, _, _, _, _, _, _, _, _ := parseArgs([]string{"--print-env"})
	if !printEnv {
		t.Error("expected printEnv=true for --print-env")
	}
}

func TestParseArgs_Version(t *testing.T) {
	_, version, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _ := parseArgs([]string{"--version"})
	if !version {
		t.Error("expected version=true for --version")
	}
}

func TestParseArgs_Verbose(t *testing.T) {
	verbose, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _ := parseArgs([]string{"--verbose"})
	if !verbose {
		t.Error("expected verbose=true for --verbose")
	}
}

func TestParseArgs_VerboseShort(t *testing.T) {
	verbose, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _ := parseArgs([]string{"-v"})
	if !verbose {
		t.Error("expected verbose=true for -v")
	}
}

func TestParseArgs_LogFile(t *testing.T) {
	_, _, _, _, _, _, _, _, logFile, _, _, _, _, _, _, _, _ := parseArgs([]string{"--log-file", "/tmp/test.log"})
	if logFile != "/tmp/test.log" {
		t.Errorf("expected logFile=%q, got %q", "/tmp/test.log", logFile)
	}
}

func TestParseArgs_LogFileEquals(t *testing.T) {
	_, _, _, _, _, _, _, _, logFile, _, _, _, _, _, _, _, _ := parseArgs([]string{"--log-file=/tmp/test.log"})
	if logFile != "/tmp/test.log" {
		t.Errorf("expected logFile=%q, got %q", "/tmp/test.log", logFile)
	}
}

func TestParseArgs_Upstream(t *testing.T) {
	_, _, _, _, _, _, _, upstream, _, _, _, _, _, _, _, _, _ := parseArgs([]string{"--upstream", "https://gw.example.com/openai/v1"})
	if upstream != "https://gw.example.com/openai/v1" {
		t.Errorf("expected upstream=%q, got %q", "https://gw.example.com/openai/v1", upstream)
	}
}

func TestParseArgs_UpstreamEquals(t *testing.T) {
	_, _, _, _, _, _, _, upstream, _, _, _, _, _, _, _, _, _ := parseArgs([]string{"--upstream=https://gw.example.com/openai/v1"})
	if upstream != "https://gw.example.com/openai/v1" {
		t.Errorf("expected upstream=%q, got %q", "https://gw.example.com/openai/v1", upstream)
	}
}

func TestParseArgs_NoOtel(t *testing.T) {
	_, _, _, _, noOtel, _, _, _, _, _, _, _, _, _, _, _, codexArgs := parseArgs([]string{"--no-otel"})
	if !noOtel {
		t.Error("expected noOtel=true for --no-otel")
	}
	if len(codexArgs) != 0 {
		t.Errorf("expected no codexArgs, got %v", codexArgs)
	}
}

func TestParseArgs_OtelLogsTable(t *testing.T) {
	_, _, _, _, _, otelLogsTable, otelLogsTableSet, _, _, _, _, _, _, _, _, _, _ := parseArgs([]string{"--otel-logs-table", "main.custom.logs"})
	if !otelLogsTableSet {
		t.Error("expected otelLogsTableSet=true when --otel-logs-table is passed")
	}
	if otelLogsTable != "main.custom.logs" {
		t.Errorf("expected otelLogsTable=%q, got %q", "main.custom.logs", otelLogsTable)
	}
}
func TestParseArgs_OtelLogsTableDefault(t *testing.T) {
	_, _, _, _, _, otelLogsTable, otelLogsTableSet, _, _, _, _, _, _, _, _, _, _ := parseArgs([]string{})
	if otelLogsTableSet {
		t.Error("expected otelLogsTableSet=false when --otel-logs-table is not passed")
	}
	_ = otelLogsTable // default applied in main(), not parseArgs
}

func TestParseArgs_UnknownFlagPassthrough(t *testing.T) {
	_, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, codexArgs := parseArgs([]string{"--unknown"})
	if len(codexArgs) != 1 || codexArgs[0] != "--unknown" {
		t.Errorf("expected codexArgs=[\"--unknown\"], got %v", codexArgs)
	}
}

func TestParseArgs_EmptyArgs(t *testing.T) {
	verbose, version, showHelp, printEnv, noOtel, _, _, upstream, logFile, profile, otel, _, _, _, _, _, codexArgs := parseArgs([]string{})
	if verbose || version || showHelp || printEnv || noOtel || otel {
		t.Error("expected all bool flags false for empty args")
	}
	if upstream != "" {
		t.Errorf("expected empty upstream, got %q", upstream)
	}
	if logFile != "" {
		t.Errorf("expected empty logFile, got %q", logFile)
	}
	if profile != "" {
		t.Errorf("expected empty profile, got %q", profile)
	}
	if len(codexArgs) != 0 {
		t.Errorf("expected no codexArgs, got %v", codexArgs)
	}
}

func TestParseArgs_Mixed(t *testing.T) {
	verbose, _, showHelp, _, _, _, _, upstream, _, _, _, _, _, _, _, _, _ := parseArgs([]string{"--verbose", "--upstream", "https://gw.example.com", "--help"})
	if !showHelp {
		t.Error("expected showHelp=true")
	}
	if !verbose {
		t.Error("expected verbose=true")
	}
	if upstream != "https://gw.example.com" {
		t.Errorf("expected upstream=%q, got %q", "https://gw.example.com", upstream)
	}
}

func TestParseArgs_Separator(t *testing.T) {
	verbose, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, codexArgs := parseArgs([]string{"--verbose", "--", "--unknown", "arg1"})
	if !verbose {
		t.Error("expected verbose=true before separator")
	}
	if len(codexArgs) != 2 || codexArgs[0] != "--unknown" || codexArgs[1] != "arg1" {
		t.Errorf("expected codexArgs=[\"--unknown\", \"arg1\"], got %v", codexArgs)
	}
}

func TestParseArgs_PassthroughArgs(t *testing.T) {
	_, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, codexArgs := parseArgs([]string{"prompt text", "--unknown-flag", "gpt-4"})
	if len(codexArgs) != 3 {
		t.Errorf("expected 3 codexArgs, got %d: %v", len(codexArgs), codexArgs)
	}
}

// --- Model flag tests ---

func TestParseArgs_Model(t *testing.T) {
	_, _, _, _, _, _, _, _, _, _, _, _, _, _, model, modelSet, _ := parseArgs([]string{"--model", "databricks-gpt-5-4-mini"})
	if !modelSet {
		t.Error("expected modelSet=true when --model is passed")
	}
	if model != "databricks-gpt-5-4-mini" {
		t.Errorf("expected model=%q, got %q", "databricks-gpt-5-4-mini", model)
	}
}

func TestParseArgs_ModelEquals(t *testing.T) {
	_, _, _, _, _, _, _, _, _, _, _, _, _, _, model, modelSet, _ := parseArgs([]string{"--model=custom-model"})
	if !modelSet {
		t.Error("expected modelSet=true when --model=value is passed")
	}
	if model != "custom-model" {
		t.Errorf("expected model=%q, got %q", "custom-model", model)
	}
}

func TestParseArgs_ModelDefault(t *testing.T) {
	_, _, _, _, _, _, _, _, _, _, _, _, _, _, model, modelSet, _ := parseArgs([]string{})
	if modelSet {
		t.Error("expected modelSet=false when --model is not passed")
	}
	if model != "" {
		t.Errorf("expected empty model from parseArgs, got %q", model)
	}
}

func TestParseArgs_ModelNotPassedThrough(t *testing.T) {
	_, _, _, _, _, _, _, _, _, _, _, _, _, _, model, modelSet, codexArgs := parseArgs([]string{"--model", "my-model", "prompt"})
	if !modelSet {
		t.Error("expected modelSet=true")
	}
	if model != "my-model" {
		t.Errorf("expected model=%q, got %q", "my-model", model)
	}
	if len(codexArgs) != 1 || codexArgs[0] != "prompt" {
		t.Errorf("expected codexArgs=[\"prompt\"], got %v", codexArgs)
	}
}

// Table-driven comprehensive test for parseArgs.
func TestParseArgs_Table(t *testing.T) {
	type result struct {
		verbose  bool
		version  bool
		showHelp bool
		printEnv bool
		noOtel   bool
		upstream string
		logFile  string
		profile  string
		otel     bool
		model    string
		modelSet bool
		codexLen int
	}

	tests := []struct {
		name string
		args []string
		want result
	}{
		{
			name: "--help sets showHelp",
			args: []string{"--help"},
			want: result{showHelp: true},
		},
		{
			name: "-h sets showHelp",
			args: []string{"-h"},
			want: result{showHelp: true},
		},
		{
			name: "--print-env sets printEnv",
			args: []string{"--print-env"},
			want: result{printEnv: true},
		},
		{
			name: "--version sets version",
			args: []string{"--version"},
			want: result{version: true},
		},
		{
			name: "--verbose sets verbose",
			args: []string{"--verbose"},
			want: result{verbose: true},
		},
		{
			name: "-v sets verbose",
			args: []string{"-v"},
			want: result{verbose: true},
		},
		{
			name: "--log-file sets logFile",
			args: []string{"--log-file", "/tmp/test.log"},
			want: result{logFile: "/tmp/test.log"},
		},
		{
			name: "--log-file=value sets logFile",
			args: []string{"--log-file=/tmp/test.log"},
			want: result{logFile: "/tmp/test.log"},
		},
		{
			name: "--upstream sets upstream",
			args: []string{"--upstream", "https://gw.example.com"},
			want: result{upstream: "https://gw.example.com"},
		},
		{
			name: "--no-otel sets noOtel",
			args: []string{"--no-otel"},
			want: result{noOtel: true},
		},
		{
			name: "unknown flag passes through",
			args: []string{"--unknown"},
			want: result{codexLen: 1},
		},
		{
			name: "empty args all defaults",
			args: []string{},
			want: result{},
		},
		{
			name: "mixed flags: verbose, upstream, help",
			args: []string{"--verbose", "--upstream", "https://gw.example.com", "--help"},
			want: result{showHelp: true, verbose: true, upstream: "https://gw.example.com"},
		},
		{
			name: "--profile sets profile",
			args: []string{"--profile", "aidev"},
			want: result{profile: "aidev"},
		},
		{
			name: "--profile=value sets profile",
			args: []string{"--profile=aidev"},
			want: result{profile: "aidev"},
		},
		{
			name: "--otel sets otel",
			args: []string{"--otel"},
			want: result{otel: true},
		},
		{
			name: "--otel and --no-otel both parsed",
			args: []string{"--otel", "--no-otel"},
			want: result{otel: true, noOtel: true},
		},
		{
			name: "--model sets model",
			args: []string{"--model", "my-model"},
			want: result{model: "my-model", modelSet: true},
		},
		{
			name: "--model=value sets model",
			args: []string{"--model=my-model"},
			want: result{model: "my-model", modelSet: true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			verbose, version, showHelp, printEnv, noOtel, _, _, upstream, logFile, profile, otel, _, _, _, model, modelSet, codexArgs := parseArgs(tc.args)

			if verbose != tc.want.verbose {
				t.Errorf("verbose: got %v, want %v", verbose, tc.want.verbose)
			}
			if version != tc.want.version {
				t.Errorf("version: got %v, want %v", version, tc.want.version)
			}
			if showHelp != tc.want.showHelp {
				t.Errorf("showHelp: got %v, want %v", showHelp, tc.want.showHelp)
			}
			if printEnv != tc.want.printEnv {
				t.Errorf("printEnv: got %v, want %v", printEnv, tc.want.printEnv)
			}
			if noOtel != tc.want.noOtel {
				t.Errorf("noOtel: got %v, want %v", noOtel, tc.want.noOtel)
			}
			if upstream != tc.want.upstream {
				t.Errorf("upstream: got %q, want %q", upstream, tc.want.upstream)
			}
			if logFile != tc.want.logFile {
				t.Errorf("logFile: got %q, want %q", logFile, tc.want.logFile)
			}
			if profile != tc.want.profile {
				t.Errorf("profile: got %q, want %q", profile, tc.want.profile)
			}
			if otel != tc.want.otel {
				t.Errorf("otel: got %v, want %v", otel, tc.want.otel)
			}
			if model != tc.want.model {
				t.Errorf("model: got %q, want %q", model, tc.want.model)
			}
			if modelSet != tc.want.modelSet {
				t.Errorf("modelSet: got %v, want %v", modelSet, tc.want.modelSet)
			}
			if len(codexArgs) != tc.want.codexLen {
				t.Errorf("codexArgs length: got %d, want %d (args: %v)", len(codexArgs), tc.want.codexLen, codexArgs)
			}
		})
	}
}

// --- default log discard test ---

func TestDefaultLogDiscard(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	var buf bytes.Buffer
	log.SetOutput(io.Discard)
	log.Print("this should be discarded")

	log.SetOutput(&buf)
	log.Print("this should appear")

	if !strings.Contains(buf.String(), "this should appear") {
		t.Error("expected log output after switching from Discard")
	}
}

// --- handlePrintEnv tests ---

func captureStdout(fn func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func TestHandlePrintEnv_DapiTokenRedacted(t *testing.T) {
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", "dapi-abc123secret", "DEFAULT", "databricks-gpt-5-4", "main.codex_telemetry.codex_otel_logs")
	})
	if !strings.Contains(out, "dapi-***") {
		t.Errorf("expected dapi token to appear as 'dapi-***', got:\n%s", out)
	}
	if strings.Contains(out, "dapi-abc123secret") {
		t.Errorf("raw dapi token should not appear in output, got:\n%s", out)
	}
}

func TestHandlePrintEnv_NonDapiTokenRedacted(t *testing.T) {
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", "eyJhbGciOiJSUzI1NiJ9", "DEFAULT", "databricks-gpt-5-4", "main.codex_telemetry.codex_otel_logs")
	})
	if !strings.Contains(out, "**** (redacted)") {
		t.Errorf("expected non-dapi token to appear as '**** (redacted)', got:\n%s", out)
	}
}

func TestHandlePrintEnv_ContainsProfile(t *testing.T) {
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", "tok", "aidev", "databricks-gpt-5-4", "main.codex_telemetry.codex_otel_logs")
	})
	if !strings.Contains(out, "aidev") {
		t.Errorf("expected output to contain profile 'aidev', got:\n%s", out)
	}
}

func TestHandlePrintEnv_ContainsDatabricksHost(t *testing.T) {
	host := "https://dbc-abc123.cloud.databricks.com"
	out := captureStdout(func() {
		handlePrintEnv(host, "https://gw.example.com/openai/v1", "tok", "DEFAULT", "databricks-gpt-5-4", "main.codex_telemetry.codex_otel_logs")
	})
	if !strings.Contains(out, host) {
		t.Errorf("expected output to contain DATABRICKS_HOST %q, got:\n%s", host, out)
	}
}

func TestHandlePrintEnv_ContainsOpenAIBaseURL(t *testing.T) {
	baseURL := "https://gw.example.com/openai/v1"
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", baseURL, "tok", "DEFAULT", "databricks-gpt-5-4", "main.codex_telemetry.codex_otel_logs")
	})
	if !strings.Contains(out, baseURL) {
		t.Errorf("expected output to contain OPENAI_BASE_URL %q, got:\n%s", baseURL, out)
	}
}

func TestHandlePrintEnv_ContainsModel(t *testing.T) {
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", "tok", "DEFAULT", "databricks-gpt-5-4-mini", "main.codex_telemetry.codex_otel_logs")
	})
	if !strings.Contains(out, "databricks-gpt-5-4-mini") {
		t.Errorf("expected output to contain model 'databricks-gpt-5-4-mini', got:\n%s", out)
	}
	if !strings.Contains(out, "Model:") {
		t.Errorf("expected output to contain 'Model:' label, got:\n%s", out)
	}
}

// --- handleHelp tests ---

func TestHandleHelp_ContainsDatabricksCodex(t *testing.T) {
	out := captureStdout(func() {
		handleHelp("")
	})
	if !strings.Contains(out, "databricks-codex") {
		t.Errorf("expected help output to contain 'databricks-codex', got:\n%s", out)
	}
}

func TestHandleHelp_ContainsPrintEnvFlag(t *testing.T) {
	out := captureStdout(func() {
		handleHelp("")
	})
	if !strings.Contains(out, "--print-env") {
		t.Errorf("expected help output to contain '--print-env', got:\n%s", out)
	}
}

func TestHandleHelp_ContainsCodexCLISeparator(t *testing.T) {
	out := captureStdout(func() {
		handleHelp("")
	})
	if !strings.Contains(out, "Codex CLI Options:") {
		t.Errorf("expected help output to contain 'Codex CLI Options:', got:\n%s", out)
	}
}

func TestParseArgs_Profile(t *testing.T) {
	_, _, _, _, _, _, _, _, _, profile, _, _, _, _, _, _, _ := parseArgs([]string{"--profile", "aidev"})
	if profile != "aidev" {
		t.Errorf("expected profile=%q, got %q", "aidev", profile)
	}
}

func TestParseArgs_ProfileEquals(t *testing.T) {
	_, _, _, _, _, _, _, _, _, profile, _, _, _, _, _, _, _ := parseArgs([]string{"--profile=production"})
	if profile != "production" {
		t.Errorf("expected profile=%q, got %q", "production", profile)
	}
}

func TestParseArgs_Otel(t *testing.T) {
	_, _, _, _, _, _, _, _, _, _, otel, _, _, _, _, _, _ := parseArgs([]string{"--otel"})
	if !otel {
		t.Error("expected otel=true for --otel")
	}
}

func TestHandleHelp_AllFlagsPresent(t *testing.T) {
	out := captureStdout(func() {
		handleHelp("")
	})
	flags := []string{"--profile", "--model", "--upstream", "--verbose", "-v", "--log-file", "--otel", "--no-otel", "--otel-logs-table", "--version", "--help"}
	for _, flag := range flags {
		if !strings.Contains(out, flag) {
			t.Errorf("expected help output to contain flag %q, got:\n%s", flag, out)
		}
	}
}

func TestHandleHelp_ContainsVersion(t *testing.T) {
	out := captureStdout(func() {
		handleHelp("")
	})
	if !strings.Contains(out, fmt.Sprintf("databricks-codex v%s", Version)) {
		t.Errorf("expected help output to contain version string, got:\n%s", out)
	}
}

// --- resolveOtelLogsTable tests ---

func TestResolveOtelLogsTable(t *testing.T) {
	tests := []struct {
		name       string
		flagValue  string
		flagSet    bool
		savedValue string
		want       string
	}{
		{
			name:      "flag set with value wins",
			flagValue: "custom.db.table",
			flagSet:   true,
			want:      "custom.db.table",
		},
		{
			name:       "flag set wins over saved",
			flagValue:  "flag.table",
			flagSet:    true,
			savedValue: "saved.table",
			want:       "flag.table",
		},
		{
			name:       "saved value used when flag not set",
			flagSet:    false,
			savedValue: "saved.table",
			want:       "saved.table",
		},
		{
			name:    "default when no flag and no saved",
			flagSet: false,
			want:    "main.codex_telemetry.codex_otel_logs",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveOtelLogsTable(tc.flagValue, tc.flagSet, tc.savedValue)
			if got != tc.want {
				t.Errorf("resolveOtelLogsTable(%q, %v, %q) = %q, want %q",
					tc.flagValue, tc.flagSet, tc.savedValue, got, tc.want)
			}
		})
	}
}

func TestResolveOtelLogsTable_NoOtelDoesNotClear(t *testing.T) {
	got := resolveOtelLogsTable("", false, "custom.table")
	if got != "custom.table" {
		t.Errorf("expected saved value to survive, got %q", got)
	}
}

func TestHandlePrintEnv_ContainsOtelLogsTable(t *testing.T) {
	table := "main.custom.otel_logs"
	out := captureStdout(func() {
		handlePrintEnv("https://dbc.example.com", "https://gw.example.com/openai/v1", "tok", "DEFAULT", "databricks-gpt-5-4", table)
	})
	if !strings.Contains(out, table) {
		t.Errorf("expected output to contain OTEL Logs Table %q, got:\n%s", table, out)
	}
	if !strings.Contains(out, "OTEL Logs Table:") {
		t.Errorf("expected output to contain 'OTEL Logs Table:' label, got:\n%s", out)
	}
}
