// Package service wires the store and runner into the MCP tools and REST
// handlers exposed by the Go main server.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"pdfeditor/server/internal/runner"
	"pdfeditor/server/internal/storage"
	"pdfeditor/server/internal/store"
	"pdfeditor/server/internal/ws"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Service holds the store and runner shared by MCP tools and REST handlers.
type Service struct {
	store  store.Store
	runner *runner.Runner
	hub    *ws.Hub
	assets storage.Storage
	outDir string

	// renderLocks holds one mutex per document, so renders of different docs
	// proceed in parallel. TODO: use dlocks if this is going to be horizontally updated.
	renderLocks sync.Map // doc id -> *sync.Mutex
}

// New returns a Service backed by st and r, writing PDFs into outDir, storing
// assets through assets, and publishing change events through hub.
func New(st store.Store, r *runner.Runner, hub *ws.Hub, outDir string, assets storage.Storage) *Service {
	return &Service{
		store:  st,
		runner: r,
		hub:    hub,
		assets: assets,
		outDir: outDir,
	}
}

// CurrentVersion returns the head version of a document, if it exists.
func (s *Service) CurrentVersion(docID string) (int, bool) {
	doc, err := s.store.Get(docID)
	if err != nil {
		return 0, false
	}
	return doc.Current().Version, true
}

// Templates are the server-side scripts a document can be created from. The
// FE references a template by name; it never sends script text.
var Templates = map[string]string{
	"empty": `from doc import Doc

doc = Doc(OUT_PATH)
doc.save()
`,
	"report": `from doc import Doc

doc = Doc(OUT_PATH)

doc.heading("Quarterly Sales Report", "title")

doc.paragraph(
    "This report summarizes revenue across regions for the quarter. "
    "Select any element and ask the assistant to change it.",
    "intro",
)

doc.table(
    [
        ["Region", "Revenue", "Growth"],
        ["North", "$120k", "+12%"],
        ["South", "$80k", "+8%"],
        ["East", "$95k", "+15%"],
        ["West", "$60k", "+5%"],
    ],
    "sales-table",
)

doc.save()
`,
	"slideshow": `from doc import Doc
from reportlab.lib.colors import HexColor

SLIDE_W, SLIDE_H = 960, 540
BG    = HexColor("#0A0F16")
PANEL = HexColor("#121B27")
HEAD  = HexColor("#172231")
LINE  = HexColor("#1E2C3D")
TEAL  = HexColor("#3EE6D0")
TEXT  = HexColor("#E9F0F7")
MUTED = HexColor("#9FB0C0")


def _bg(canvas, doc):
    canvas.saveState()
    canvas.setFillColor(BG)
    canvas.rect(0, 0, SLIDE_W, SLIDE_H, stroke=0, fill=1)
    canvas.restoreState()


doc = Doc(OUT_PATH, pagesize=(SLIDE_W, SLIDE_H), on_page=_bg)

doc.styles["Heading1"].fontName = "Helvetica-Bold"
doc.styles["Heading1"].fontSize = 30
doc.styles["Heading1"].leading = 34
doc.styles["Heading1"].textColor = TEAL
doc.styles["Heading1"].spaceAfter = 16

doc.styles["BodyText"].fontName = "Helvetica"
doc.styles["BodyText"].fontSize = 15
doc.styles["BodyText"].leading = 22
doc.styles["BodyText"].textColor = TEXT

# Slide 1 — title
doc.spacer(90, "slide1-top")
doc.heading("IT Presentation", "slide1-title")
doc.paragraph("A generic four-slide overview.", "slide1-subtitle")
doc.page_break("slide1-break")

# Slide 2 — agenda
doc.heading("Agenda", "slide2-title")
doc.paragraph(
    "1. Introduction<br/>2. Architecture<br/>3. Key Metrics<br/>4. Next Steps",
    "slide2-agenda",
)
doc.page_break("slide2-break")

# Slide 3 — architecture
doc.heading("Architecture", "slide3-title")
doc.table(
    [
        ["Layer", "Responsibility"],
        ["Client", "User interface and interaction"],
        ["Service", "Business logic and orchestration"],
        ["Data", "Persistence and caching"],
    ],
    "slide3-table",
    style=[
        ("GRID", (0, 0), (-1, -1), 0.5, LINE),
        ("BACKGROUND", (0, 0), (-1, 0), HEAD),
        ("BACKGROUND", (0, 1), (-1, -1), PANEL),
        ("FONTNAME", (0, 0), (-1, 0), "Helvetica-Bold"),
        ("TEXTCOLOR", (0, 0), (-1, 0), TEAL),
        ("TEXTCOLOR", (0, 1), (-1, -1), TEXT),
        ("VALIGN", (0, 0), (-1, -1), "MIDDLE"),
        ("TOPPADDING", (0, 0), (-1, -1), 8),
        ("BOTTOMPADDING", (0, 0), (-1, -1), 8),
    ],
)
doc.page_break("slide3-break")

# Slide 4 — next steps
doc.heading("Next Steps", "slide4-title")
doc.paragraph(
    "1. Refine content<br/>2. Wire editing tools<br/>3. Ship the viewer",
    "slide4-steps",
)

doc.save()
`,
}

// Seed creates a fixed-id document from a template and renders it. It backs
// the dev/test document the FE can rely on.
func (s *Service) Seed(id, templateName string) error {
	script, ok := Templates[templateName]
	if !ok {
		return fmt.Errorf("unknown template %q", templateName)
	}
	if _, err := s.store.Seed(id, script); err != nil {
		return err
	}
	res := s.run(id, 1)
	log.Printf("seeded doc %q (template %s): %s", id, templateName, resultText(res))
	return nil
}

// ErrCode is a stable machine-readable error code exposed to the FE.
type ErrCode string

const (
	ErrNone       ErrCode = ""
	ErrError      ErrCode = "ERROR"
	ErrNoManifest ErrCode = "NO_MANIFEST"
)

// CodedError is an error carrying a stable machine-readable code for the FE.
type CodedError struct {
	Code ErrCode
	Msg  string
}

func (e *CodedError) Error() string { return e.Msg }

type manifestDoc struct {
	PageSize []float64 `json:"page_size"`
	Elements []element `json:"elements"`
}

type element struct {
	ID   string    `json:"id"`
	Type string    `json:"type"`
	Page int       `json:"page"`
	BBox []float64 `json:"bbox"`
	Line int       `json:"line"`
}

func (s *Service) docVersion(doc store.Document, version int) (store.ScriptVersion, bool) {
	for _, v := range doc.Versions {
		if v.Version == version {
			return v, true
		}
	}
	return store.ScriptVersion{}, false
}

func (s *Service) manifestFor(doc store.Document, version int) (manifestDoc, error) {
	var m manifestDoc
	sv, ok := s.docVersion(doc, version)
	if !ok {
		return m, fmt.Errorf("version %d not found", version)
	}
	if sv.Manifest == "" {
		return m, &CodedError{Code: ErrNoManifest, Msg: fmt.Sprintf("version %d has no rendered manifest yet; run it first", version)}
	}
	if err := json.Unmarshal([]byte(sv.Manifest), &m); err != nil {
		return m, fmt.Errorf("parse manifest: %w", err)
	}
	return m, nil
}

// surroundingLines returns lines [line-3, line+3] of script (1-indexed).
func surroundingLines(script string, line int) string {
	lines := strings.Split(script, "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	lo := line - 4
	if lo < 0 {
		lo = 0
	}
	hi := line + 3
	if hi > len(lines) {
		hi = len(lines)
	}
	var b strings.Builder
	for i := lo; i < hi; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i+1, lines[i])
	}
	return b.String()
}

func jsonResult(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}
	return mcp.NewToolResultText(string(b))
}

// resultText returns the text content of a tool result, or "".
func resultText(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

// ---- MCP tool handlers ----

func (s *Service) getScript(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	docID, err := req.RequireString("doc_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	doc, err := s.store.Get(docID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	cur := doc.Current()
	return jsonResult(map[string]any{"doc_id": doc.ID, "version": cur.Version, "script": doc.Draft}), nil
}

func (s *Service) getElement(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	docID, err := req.RequireString("doc_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	elementID, err := req.RequireString("element_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	doc, err := s.store.Get(docID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	cur := doc.Current()
	m, err := s.manifestFor(doc, cur.Version)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var matches []element
	for _, e := range m.Elements {
		if e.ID == elementID {
			matches = append(matches, e)
		}
	}
	if len(matches) == 0 {
		return mcp.NewToolResultError(fmt.Sprintf("element %q not found in document %q", elementID, docID)), nil
	}
	return jsonResult(map[string]any{
		"doc_id":       docID,
		"element_id":   elementID,
		"entries":      matches,
		"script_lines": surroundingLines(cur.Script, matches[0].Line),
	}), nil
}

func (s *Service) editScript(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	docID, err := req.RequireString("doc_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	baseVersion, err := req.RequireInt("base_version")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	script, err := req.RequireString("script")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err := s.store.Edit(docID, baseVersion, script); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{"doc_id": docID, "version": baseVersion, "committed": false}), nil
}

func (s *Service) render(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	docID, err := req.RequireString("doc_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	v, err := s.store.Commit(docID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return s.run(docID, v.Version), nil
}

func (s *Service) listVersions(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	docID, err := req.RequireString("doc_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	vs, err := s.store.ListVersions(docID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{"doc_id": docID, "versions": vs}), nil
}

func (s *Service) restoreVersion(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	docID, err := req.RequireString("doc_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	version, err := req.RequireInt("version")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	v, err := s.store.Restore(docID, version)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{"doc_id": docID, "version": v.Version, "committed": false}), nil
}

func (s *Service) listElements(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	docID, err := req.RequireString("doc_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	doc, err := s.store.Get(docID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	m, err := s.manifestFor(doc, doc.Current().Version)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{"doc_id": docID, "page_size": m.PageSize, "elements": m.Elements}), nil
}

// editingPdf notifies the FE (loading gear) that a document is being edited.
func (s *Service) editingPdf(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	docID, err := req.RequireString("doc_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	s.hub.Publish(docID, ws.UpdatingPDF(docID))
	return jsonResult(map[string]any{"doc_id": docID, "editing": true}), nil
}

// addSvg stores an SVG asset for a document and returns its URI. The script
// then references it with doc.svg(name, element_id).
func (s *Service) addSvg(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	docID, err := req.RequireString("doc_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	name, err := req.RequireString("name")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	svg, err := req.RequireString("svg")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	name = filepath.Base(name)
	uri, err := s.assets.Write(context.Background(), filepath.Join(docID, name), []byte(svg))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{"doc_id": docID, "name": name, "uri": uri}), nil
}

// run renders a version under renderLock and publishes updating_pdf before,
// then pdf_updated or pdf_failed after.
func (s *Service) run(docID string, version int) *mcp.CallToolResult {
	doc, err := s.store.Get(docID)
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}
	sv, ok := s.docVersion(doc, version)
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("version %d not found in document %q", version, docID))
	}

	mu, _ := s.renderLocks.LoadOrStore(docID, &sync.Mutex{})
	renderLock := mu.(*sync.Mutex)
	renderLock.Lock()
	defer renderLock.Unlock()

	s.hub.Publish(docID, ws.UpdatingPDF(docID))

	res, err := s.runner.Run(sv.Script, s.assets.Root(docID))
	if err != nil {
		s.hub.Publish(docID, ws.PDFFailed(docID, version, err.Error()))
		return mcp.NewToolResultError(err.Error())
	}
	if !res.OK {
		s.hub.Publish(docID, ws.PDFFailed(docID, version, res.Traceback))
		return jsonResult(map[string]any{"ok": false, "traceback": res.Traceback})
	}

	pdfPath := filepath.Join(s.outDir, fmt.Sprintf("%s-v%d.pdf", docID, version))
	if err := os.MkdirAll(s.outDir, 0o755); err != nil {
		s.hub.Publish(docID, ws.PDFFailed(docID, version, err.Error()))
		return mcp.NewToolResultError(err.Error())
	}
	if err := os.WriteFile(pdfPath, res.PDF, 0o644); err != nil {
		s.hub.Publish(docID, ws.PDFFailed(docID, version, err.Error()))
		return mcp.NewToolResultError(err.Error())
	}
	if err := s.store.RecordOutput(docID, version, pdfPath, res.Manifest); err != nil {
		s.hub.Publish(docID, ws.PDFFailed(docID, version, err.Error()))
		return mcp.NewToolResultError(err.Error())
	}
	s.hub.Publish(docID, ws.PDFUpdated(docID, version,
		fmt.Sprintf("/docs/%s/pdf?version=%d", docID, version),
		fmt.Sprintf("/docs/%s/manifest", docID)))
	return jsonResult(map[string]any{
		"ok":       true,
		"version":  version,
		"pdf_path": pdfPath,
		"manifest": json.RawMessage(res.Manifest),
	})
}

// RegisterTools adds the document tools to an MCP server.
func (s *Service) RegisterTools(mcpSrv *server.MCPServer) {
	type spec struct {
		name, desc string
		args       []mcp.ToolOption
		handler    server.ToolHandlerFunc
	}
	docID := mcp.WithString("doc_id", mcp.Required(), mcp.Description("The document id."))
	tools := []spec{
		{
			"get_script", "Return the current script text and version for a document.",
			[]mcp.ToolOption{docID},
			s.getScript,
		},
		{
			"get_element", "Return a manifest entry and its surrounding script lines for one element.",
			[]mcp.ToolOption{docID, mcp.WithString("element_id", mcp.Required(), mcp.Description("The element id."))},
			s.getElement,
		},
		{
			"edit_script", "Store a new script version. Rejects a stale base_version.",
			[]mcp.ToolOption{
				docID,
				mcp.WithInteger("base_version", mcp.Required(), mcp.Description("The version this edit is based on.")),
				mcp.WithString("script", mcp.Required(), mcp.Description("The full new script text.")),
			},
			s.editScript,
		},
		{
			"render", "Commit the draft script as a new version, render it to PDF, and return the manifest (synchronous).",
			[]mcp.ToolOption{docID},
			s.render,
		},
		{
			"editing_pdf", "Notify the UI that a document is being edited (show a loading indicator).",
			[]mcp.ToolOption{docID},
			s.editingPdf,
		},
		{
			"add_svg", "Store an SVG asset for a document and return its URI; reference it in the script with doc.svg(name, element_id).",
			[]mcp.ToolOption{
				docID,
				mcp.WithString("name", mcp.Required(), mcp.Description("The asset filename, e.g. chart.svg.")),
				mcp.WithString("svg", mcp.Required(), mcp.Description("The SVG markup.")),
			},
			s.addSvg,
		},
		{
			"list_versions", "List all script versions of a document, oldest first.",
			[]mcp.ToolOption{docID},
			s.listVersions,
		},
		{
			"restore_version", "Set the draft to an older version's script (undo); committed on render.",
			[]mcp.ToolOption{docID, mcp.WithInteger("version", mcp.Required(), mcp.Description("The version to restore."))},
			s.restoreVersion,
		},
		{
			"list_elements", "List the document's manifest elements (id, type, page, bbox, line).",
			[]mcp.ToolOption{docID},
			s.listElements,
		},
	}
	for _, t := range tools {
		opts := append([]mcp.ToolOption{mcp.WithDescription(t.desc)}, t.args...)
		mcpSrv.AddTool(mcp.NewTool(t.name, opts...), t.handler)
	}
}
