// Package runner executes an AI-written Doc script as a subprocess.
//
// It runs with a timeout, a temp working directory, and a scrubbed
// environment (no host secrets). Tracebacks are returned verbatim so the
// caller can relay them to the model. This is the single choke point the
// Phase 6 sandbox will replace.
package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Runner runs scripts through the gen/run.py wrapper.
type Runner struct {
	PyPath  string // path to gen/run.py
	Python  string // python3 binary, default "python3"
	Timeout time.Duration
}

// New returns a Runner.
func New(pyPath, python string) *Runner {
	if python == "" {
		python = "python3"
	}
	return &Runner{PyPath: pyPath, Python: python, Timeout: 30 * time.Second}
}

// Result is the outcome of one run.
type Result struct {
	OK        bool   `json:"ok"`
	Manifest  string `json:"manifest,omitempty"`
	PDF       []byte `json:"-"` // rendered PDF bytes; not serialized to JSON
	Traceback string `json:"traceback,omitempty"`
}

// Run executes script and returns the rendered PDF + manifest or a traceback.
// assetRoot is the URI of the document's asset directory (file:// or s3://);
// it is exposed to the script as PDF_ASSET_ROOT so it can load assets.
func (r *Runner) Run(script, assetRoot string) (Result, error) {
	dir, err := os.MkdirTemp("", "pdfgen-")
	if err != nil {
		return Result{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	scriptPath := filepath.Join(dir, "script.py")
	outPath := filepath.Join(dir, "out.pdf")
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		return Result{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()

	pyPath, err := filepath.Abs(r.PyPath)
	if err != nil {
		return Result{}, fmt.Errorf("resolve runner path: %w", err)
	}
	cmd := exec.CommandContext(ctx, r.Python, pyPath, scriptPath, outPath)
	cmd.Dir = dir
	cmd.Env = scrubbedEnv(dir, assetRoot)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	// Read the manifest on partial success too (some elements may have drawn).
	res := Result{}
	if b, err := os.ReadFile(outPath + ".manifest.json"); err == nil {
		res.Manifest = string(b)
	}
	if runErr == nil {
		pdf, err := os.ReadFile(outPath)
		if err != nil {
			return Result{}, fmt.Errorf("read rendered pdf: %w", err)
		}
		res.OK = true
		res.PDF = pdf
		return res, nil
	}

	if ctx.Err() == context.DeadlineExceeded {
		res.Traceback = "timeout after " + r.Timeout.String()
	} else {
		res.Traceback = stderr.String()
		if res.Traceback == "" {
			// exec failed to start (e.g. interpreter not found): stderr is empty.
			res.Traceback = runErr.Error()
		}
	}
	return res, nil
}

// TODO: bsena; Use another service to create a runner that is focused only in creating that
// scrubbedEnv builds a minimal environment with no host secrets.
func scrubbedEnv(home, assetRoot string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"LANG=C.UTF-8",
	}
	if assetRoot != "" {
		env = append(env, "PDF_ASSET_ROOT="+assetRoot)
	}

	// Python version selectors (pyenv/venv) are needed to resolve the right
	// interpreter; they carry no secrets.
	for _, k := range []string{"PYENV_ROOT", "PYENV_VERSION", "PYENV_SHELL", "VIRTUAL_ENV", "VIRTUAL_ENV_PROMPT"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}
