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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Runner runs scripts through the gen/run.py wrapper.
type Runner struct {
	PyPath   string // path to gen/run.py
	Python   string // python3 binary, default "python3"
	PDFToPPM string // first-page PNG renderer, default "pdftoppm"
	Timeout  time.Duration
}

// New returns a Runner.
func New(pyPath, python string) *Runner {
	if python == "" {
		python = "python3"
	}
	return &Runner{PyPath: pyPath, Python: python, PDFToPPM: "pdftoppm", Timeout: 30 * time.Second}
}

// Result is the outcome of one run.
type Result struct {
	OK         bool   `json:"ok"`
	Manifest   string `json:"manifest,omitempty"`
	SlidesJSON string `json:"slides_json,omitempty"`
	PDF        []byte `json:"-"` // rendered PDF bytes; not serialized to JSON
	PPTX       []byte `json:"-"` // editable slideshow deck; not serialized to JSON
	CoverPNG   []byte `json:"-"` // first page rendered as PNG
	Traceback  string `json:"traceback,omitempty"`
}

// Run executes script and returns the rendered PDF + manifest or a traceback.
// Assets are materialized in the temporary working directory for any driver.
func (r *Runner) Run(script string, assets map[string][]byte) (Result, error) {
	return r.RunType(script, assets, "pdf")
}

// RunType executes a document script for the requested project type.
func (r *Runner) RunType(script string, assets map[string][]byte, projectType string) (Result, error) {
	if projectType == "" {
		projectType = "pdf"
	}
	if projectType != "pdf" && projectType != "slideshow" {
		return Result{}, fmt.Errorf("unsupported project type %q", projectType)
	}
	dir, err := os.MkdirTemp("", "pdfgen-")
	if err != nil {
		return Result{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	assetDir := filepath.Join(dir, "assets")
	if err := os.Mkdir(assetDir, 0o755); err != nil {
		return Result{}, err
	}
	for name, data := range assets {
		if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
			return Result{}, fmt.Errorf("invalid asset name %q", name)
		}
		if err := os.WriteFile(filepath.Join(assetDir, name), data, 0o644); err != nil {
			return Result{}, err
		}
	}

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
	cmd := exec.CommandContext(ctx, r.Python, pyPath, scriptPath, outPath, "--type", projectType)
	cmd.Dir = dir
	cmd.Env = scrubbedEnv(dir, "file://"+assetDir)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	// Read the manifest on partial success too (some elements may have drawn).
	res := Result{}
	if b, err := os.ReadFile(outPath + ".manifest.json"); err == nil {
		res.Manifest = string(b)
	}
	if runErr == nil {
		var pptx []byte
		if projectType == "slideshow" {
			pptx, err = os.ReadFile(outPath + ".pptx")
			if err != nil {
				return Result{}, fmt.Errorf("read rendered PPTX: %w", err)
			}
			slides, err := os.ReadFile(outPath + ".slides.json")
			if err != nil {
				return Result{}, fmt.Errorf("read slide JSON: %w", err)
			}
			cover, err := os.ReadFile(outPath + ".cover.png")
			if err != nil {
				return Result{}, fmt.Errorf("read rendered slide cover: %w", err)
			}
			if err := validateSlidesJSON(slides); err != nil {
				return Result{}, err
			}
			if err := validateManifest([]byte(res.Manifest)); err != nil {
				return Result{}, err
			}
			if len(pptx) < 4 || !bytes.Equal(pptx[:2], []byte("PK")) {
				return Result{}, fmt.Errorf("rendered PPTX is invalid")
			}
			if len(cover) < 8 || !bytes.Equal(cover[:8], []byte("\x89PNG\r\n\x1a\n")) {
				return Result{}, fmt.Errorf("rendered slide cover is invalid")
			}
			res.PPTX = pptx
			res.SlidesJSON = string(slides)
		}
		pdf, err := os.ReadFile(outPath)
		if err != nil {
			return Result{}, fmt.Errorf("read rendered pdf: %w", err)
		}
		coverPrefix := filepath.Join(dir, "cover")
		pdfToPPM := r.PDFToPPM
		if pdfToPPM == "" {
			pdfToPPM = "pdftoppm"
		}
		coverCmd := exec.CommandContext(ctx, pdfToPPM, "-f", "1", "-l", "1", "-singlefile", "-png", "-scale-to", "640", outPath, coverPrefix)
		var coverErr bytes.Buffer
		coverCmd.Stderr = &coverErr
		if err := coverCmd.Run(); err != nil {
			return Result{}, fmt.Errorf("render PDF cover: %s: %w", coverErr.String(), err)
		}
		coverPNG, err := os.ReadFile(coverPrefix + ".png")
		if err != nil {
			return Result{}, fmt.Errorf("read rendered PDF cover: %w", err)
		}
		res.OK = true
		res.PDF = pdf
		res.PPTX = pptx
		res.CoverPNG = coverPNG
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

func validateSlidesJSON(data []byte) error {
	var payload struct {
		SchemaVersion int               `json:"schema_version"`
		Width         float64           `json:"width"`
		Height        float64           `json:"height"`
		FontFamily    string            `json:"font_family"`
		Fonts         map[string]string `json:"fonts"`
		Slides        []struct {
			Page int `json:"page"`
		} `json:"slides"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("malformed slides JSON: %w", err)
	}
	if payload.SchemaVersion != 1 || payload.Width <= 0 || payload.Height <= 0 || payload.FontFamily != "Vera" || payload.Fonts["regular"] != "Vera.ttf" || payload.Fonts["bold"] != "VeraBd.ttf" || len(payload.Slides) == 0 {
		return fmt.Errorf("slides JSON is missing required schema fields")
	}
	for i, slide := range payload.Slides {
		if slide.Page != i+1 {
			return fmt.Errorf("slides JSON has invalid page order")
		}
	}
	return nil
}

func validateManifest(data []byte) error {
	var manifest struct {
		PageSize []float64         `json:"page_size"`
		Elements []json.RawMessage `json:"elements"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("malformed slideshow manifest: %w", err)
	}
	if len(manifest.PageSize) != 2 || manifest.PageSize[0] <= 0 || manifest.PageSize[1] <= 0 || manifest.Elements == nil {
		return fmt.Errorf("slideshow manifest is missing required fields")
	}
	return nil
}

// Font returns the bundled Bitstream Vera font used by slide text rendering.
func (r *Runner) Font(bold bool) ([]byte, error) {
	filename := "Vera.ttf"
	if bold {
		filename = "VeraBd.ttf"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code := "import os,sys; from reportlab import rl_config; name=sys.argv[1]; paths=[os.path.join(p,name) for p in rl_config.TTFSearchPath]; path=next((p for p in paths if os.path.isfile(p)),None); sys.stdout.buffer.write(open(path,'rb').read()) if path else sys.exit('bundled font not found: '+name)"
	cmd := exec.CommandContext(ctx, r.Python, "-c", code, filename)
	cmd.Env = scrubbedEnv(os.TempDir(), "")
	var output, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("load bundled font %s: %s: %w", filename, stderr.String(), err)
	}
	return output.Bytes(), nil
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
	for _, k := range []string{"PYENV_ROOT", "PYENV_VERSION", "PYENV_SHELL", "VIRTUAL_ENV", "VIRTUAL_ENV_PROMPT", "LIBREOFFICE"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}
