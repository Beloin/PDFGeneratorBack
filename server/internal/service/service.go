// Package service wires the store and runner into the MCP tools and REST
// handlers exposed by the Go main server.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"pdfeditor/server/internal/runner"
	"pdfeditor/server/internal/storage"
	"pdfeditor/server/internal/store"
	"pdfeditor/server/internal/ws"
)

// Service holds the store and runner shared by MCP tools and REST handlers.
type Service struct {
	store  store.Store
	runner *runner.Runner
	hub    *ws.Hub
	driver storage.Driver

	// renderLocks holds one mutex per document, so renders of different docs
	// proceed in parallel. TODO: use dlocks if this is going to be horizontally updated.
	renderLocks sync.Map // doc id -> *sync.Mutex
}

// New returns a Service backed by a document store and a key-based file driver.
func New(st store.Store, r *runner.Runner, hub *ws.Hub, driver storage.Driver) *Service {
	return &Service{
		store:  st,
		runner: r,
		hub:    hub,
		driver: driver,
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
	"slideshow-pptx": slideshowScript("Presentation", "A clear story, one slide at a time.", [][2]string{{"The idea", "Introduce the central idea and explain why it matters."}, {"The plan", "Outline the next steps, owners, and expected outcomes."}, {"Next steps", "Summarize the decisions and invite discussion."}}),
	"business-pptx":  slideshowScript("Business Strategy", "A focused plan for durable growth.", [][2]string{{"Opportunity", "Define the customer problem and the market opportunity."}, {"Go-to-market", "Connect the audience, positioning, and distribution strategy."}, {"Execution", "Set priorities, assign owners, and review progress regularly."}}),
	"metrics-pptx":   slideshowScript("Metrics Review", "Turn evidence into the next decision.", [][2]string{{"Growth", "Add your acquisition, activation, and retention metrics."}, {"Performance", "Compare results with targets and explain the largest changes."}, {"Actions", "Choose the experiments and priorities supported by the data."}}),
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
	"business": `from doc import Doc
from reportlab.lib.colors import HexColor

W, H = 960, 540
NAVY = HexColor("#17283F")
ORANGE = HexColor("#D96C3A")
CREAM = HexColor("#F7F5F0")
PALE = HexColor("#E9EDEB")
INK = HexColor("#263748")


def background(canvas, _):
    canvas.saveState()
    canvas.setFillColor(CREAM)
    canvas.rect(0, 0, W, H, stroke=0, fill=1)
    canvas.setFillColor(NAVY)
    canvas.rect(0, H - 14, W, 14, stroke=0, fill=1)
    canvas.setFillColor(ORANGE)
    canvas.rect(0, H - 14, 190, 14, stroke=0, fill=1)
    canvas.setStrokeColor(PALE)
    canvas.line(72, 52, W - 72, 52)
    canvas.setFillColor(INK)
    canvas.setFont("Helvetica", 9)
    canvas.drawString(72, 34, "BUSINESS STRATEGY  /  EXAMPLE DECK")
    canvas.drawRightString(W - 72, 34, str(canvas.getPageNumber()).zfill(2))
    canvas.restoreState()


doc = Doc(OUT_PATH, pagesize=(W, H), on_page=background)
doc.styles["Heading1"].fontName = "Helvetica-Bold"
doc.styles["Heading1"].fontSize = 32
doc.styles["Heading1"].leading = 38
doc.styles["Heading1"].textColor = NAVY
doc.styles["Heading1"].spaceAfter = 18
doc.styles["BodyText"].fontName = "Helvetica"
doc.styles["BodyText"].fontSize = 17
doc.styles["BodyText"].leading = 25
doc.styles["BodyText"].textColor = INK
doc.styles["BodyText"].spaceAfter = 12

TABLE_STYLE = [
    ("BACKGROUND", (0, 0), (-1, 0), NAVY),
    ("TEXTCOLOR", (0, 0), (-1, 0), CREAM),
    ("BACKGROUND", (0, 1), (-1, -1), PALE),
    ("TEXTCOLOR", (0, 1), (-1, -1), INK),
    ("FONTNAME", (0, 0), (-1, 0), "Helvetica-Bold"),
    ("FONTNAME", (0, 1), (-1, -1), "Helvetica"),
    ("FONTSIZE", (0, 0), (-1, -1), 16),
    ("BOTTOMPADDING", (0, 0), (-1, -1), 16),
    ("TOPPADDING", (0, 0), (-1, -1), 16),
    ("LINEBELOW", (0, 0), (-1, 0), 2, ORANGE),
]

# Slide 1: thesis
doc.spacer(72, "business-cover-space")
doc.heading("Business Strategy", "business-cover-title")
doc.paragraph("A focused plan for durable growth and clearer execution.", "business-cover-subtitle")
doc.spacer(28, "business-cover-gap")
doc.paragraph("Opportunity  /  Go-to-market  /  Priorities", "business-cover-agenda")
doc.page_break("business-cover-break")

# Slide 2: opportunity
doc.heading("Market Opportunity", "business-market-title")
doc.paragraph("Start with a narrow customer problem and prove repeatable value before expanding.", "business-market-intro")
doc.table([
    ["Segment", "Need", "Initial offer"],
    ["Growing teams", "Simpler workflows", "Guided setup"],
    ["Operations", "Better visibility", "Shared dashboard"],
    ["Leadership", "Faster decisions", "Clear reporting"],
], "business-market-table", col_widths=[250, 260, 270], style=TABLE_STYLE)
doc.spacer(22, "business-market-gap")
doc.paragraph("<b>Decision:</b> Validate the first segment before expanding into adjacent ones.", "business-market-takeaway")
doc.page_break("business-market-break")

# Slide 3: execution
doc.heading("Go-to-Market", "business-gtm-title")
doc.paragraph("Build one reliable acquisition loop, then scale the channels that convert.", "business-gtm-intro")
doc.table([
    ["Channel", "Action", "Signal"],
    ["Partners", "Co-sell pilots", "Qualified leads"],
    ["Content", "Show use cases", "Demo requests"],
    ["Product", "Shorten activation", "Time to value"],
], "business-gtm-table", col_widths=[250, 260, 270], style=TABLE_STYLE)
doc.spacer(22, "business-gtm-gap")
doc.paragraph("<b>Decision:</b> Scale the channel that creates the most active customers.", "business-gtm-takeaway")
doc.page_break("business-gtm-break")

# Slide 4: priorities
doc.heading("Next 90 Days", "business-next-title")
doc.paragraph("Assign owners, measure progress weekly, and adjust from customer evidence.", "business-next-intro")
doc.table([
    ["Phase", "Priority", "Output"],
    ["Days 1-30", "Validate demand", "Customer interviews"],
    ["Days 31-60", "Launch pilot", "First active cohort"],
    ["Days 61-90", "Improve repeatability", "Channel playbook"],
], "business-next-table", col_widths=[250, 260, 270], style=TABLE_STYLE)
doc.spacer(22, "business-next-gap")
doc.paragraph("<b>Decision:</b> Review milestones weekly and stop low-signal work.", "business-next-takeaway")
doc.save()
`,
	"metrics": `from doc import Doc
from reportlab.lib.colors import HexColor

W, H = 960, 540
BG = HexColor("#101827")
PANEL = HexColor("#1B2B40")
TEAL = HexColor("#55D8C8")
BLUE = HexColor("#7FA8FF")
WHITE = HexColor("#F1F6FA")
MUTED = HexColor("#AFC1D0")


def background(canvas, _):
    canvas.saveState()
    canvas.setFillColor(BG)
    canvas.rect(0, 0, W, H, stroke=0, fill=1)
    canvas.setFillColor(TEAL)
    canvas.rect(72, H - 48, 58, 5, stroke=0, fill=1)
    canvas.setFillColor(BLUE)
    canvas.rect(136, H - 48, 24, 5, stroke=0, fill=1)
    canvas.setStrokeColor(PANEL)
    canvas.line(72, 52, W - 72, 52)
    canvas.setFillColor(MUTED)
    canvas.setFont("Helvetica", 9)
    canvas.drawString(72, 34, "METRICS REVIEW  /  ILLUSTRATIVE DATA")
    canvas.drawRightString(W - 72, 34, str(canvas.getPageNumber()).zfill(2))
    canvas.restoreState()


doc = Doc(OUT_PATH, pagesize=(W, H), on_page=background)
doc.styles["Heading1"].fontName = "Helvetica-Bold"
doc.styles["Heading1"].fontSize = 32
doc.styles["Heading1"].leading = 38
doc.styles["Heading1"].textColor = TEAL
doc.styles["Heading1"].spaceAfter = 18
doc.styles["BodyText"].fontName = "Helvetica"
doc.styles["BodyText"].fontSize = 17
doc.styles["BodyText"].leading = 25
doc.styles["BodyText"].textColor = WHITE
doc.styles["BodyText"].spaceAfter = 12

TABLE_STYLE = [
    ("BACKGROUND", (0, 0), (-1, 0), TEAL),
    ("TEXTCOLOR", (0, 0), (-1, 0), BG),
    ("BACKGROUND", (0, 1), (-1, -1), PANEL),
    ("TEXTCOLOR", (0, 1), (-1, -1), WHITE),
    ("FONTNAME", (0, 0), (-1, 0), "Helvetica-Bold"),
    ("FONTNAME", (0, 1), (-1, -1), "Helvetica"),
    ("FONTSIZE", (0, 0), (-1, -1), 16),
    ("BOTTOMPADDING", (0, 0), (-1, -1), 16),
    ("TOPPADDING", (0, 0), (-1, -1), 16),
    ("LINEBELOW", (0, 1), (-1, -2), 0.5, BG),
]

# Slide 1: overview
doc.spacer(72, "metrics-cover-space")
doc.heading("Metrics Review", "metrics-cover-title")
doc.paragraph("A four-slide snapshot of growth, revenue, and retention.", "metrics-cover-subtitle")
doc.spacer(25, "metrics-cover-gap")
doc.paragraph("Illustrative figures — replace these with your own data.", "metrics-cover-note")
doc.page_break("metrics-cover-break")

# Slide 2: headline KPIs
doc.heading("North Star Metrics", "metrics-kpi-title")
doc.paragraph("Use a small set of measures to track progress and spot tradeoffs.", "metrics-kpi-intro")
doc.table([
    ["Metric", "Current", "vs. prior period"],
    ["Active customers", "4,820", "+12%"],
    ["Monthly revenue", "$1.24M", "+18%"],
    ["Net retention", "108%", "+3 pts"],
], "metrics-kpi-table", col_widths=[280, 250, 250], style=TABLE_STYLE)
doc.spacer(22, "metrics-kpi-gap")
doc.paragraph("<b>Readout:</b> Revenue grows faster than the customer base in this example.", "metrics-kpi-takeaway")
doc.page_break("metrics-kpi-break")

# Slide 3: growth funnel
doc.heading("Growth Funnel", "metrics-funnel-title")
doc.paragraph("Follow the journey from reach to activated customers.", "metrics-funnel-intro")
doc.table([
    ["Stage", "Volume", "Conversion"],
    ["Site visitors", "92,000", "100%"],
    ["Qualified leads", "8,300", "9.0%"],
    ["New customers", "620", "7.5% of leads"],
], "metrics-funnel-table", col_widths=[280, 250, 250], style=TABLE_STYLE)
doc.spacer(22, "metrics-funnel-gap")
doc.paragraph("<b>Readout:</b> Improving activation is the clearest near-term lever.", "metrics-funnel-takeaway")
doc.page_break("metrics-funnel-break")

# Slide 4: operating health
doc.heading("Retention & Health", "metrics-health-title")
doc.paragraph("Pair growth with customer outcomes and service quality.", "metrics-health-intro")
doc.table([
    ["Indicator", "Current", "Target"],
    ["Monthly churn", "2.1%", "Below 2.5%"],
    ["Customer satisfaction", "92%", "Above 90%"],
    ["Support response", "4.2 hours", "Under 6 hours"],
], "metrics-health-table", col_widths=[280, 250, 250], style=TABLE_STYLE)
doc.spacer(22, "metrics-health-gap")
doc.paragraph("<b>Readout:</b> Protect retention as acquisition scales.", "metrics-health-takeaway")
doc.save()
`,
}

type Template struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Script   string `json:"script,omitempty"`
	CoverKey string `json:"cover_key,omitempty"`
	Type     string `json:"type"`
}

var templateNames = map[string]string{
	"slideshow-pptx": "Slideshow (PowerPoint)", "business-pptx": "Business Presentation (PowerPoint)", "metrics-pptx": "Metrics Presentation (PowerPoint)",
	"empty": "Blank", "report": "Report", "slideshow": "Slideshow",
	"business": "Business Presentation", "metrics": "Metrics Presentation",
}

func templateKey(id string) (string, error) {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\") {
		return "", fmt.Errorf("invalid template id %q", id)
	}
	return path.Join("templates", id, "template.json"), nil
}

// SeedTemplates writes bundled templates only when their file is absent.
func (s *Service) SeedTemplates() error {
	for id, script := range Templates {
		key, _ := templateKey(id)
		b, err := json.Marshal(Template{ID: id, Name: templateNames[id], Script: script, Type: templateType(id)})
		if err != nil {
			return err
		}
		if _, err := s.driver.PutIfAbsent(context.Background(), key, b); err != nil {
			return err
		}
		t, err := s.getTemplate(id)
		if err != nil {
			return err
		}
		coverKey := path.Join("templates", id, "cover.png")
		var coverErr error
		if t.CoverKey != "" {
			_, coverErr = s.driver.Get(context.Background(), t.CoverKey)
		}
		if coverErr != nil && !errors.Is(coverErr, storage.ErrNotFound) {
			return fmt.Errorf("read template %q cover: %w", id, coverErr)
		}
		if t.CoverKey == "" || errors.Is(coverErr, storage.ErrNotFound) {
			res, err := s.runner.RunType(t.Script, nil, t.Type)
			if err == nil && !res.OK {
				err = fmt.Errorf("template render failed: %s", res.Traceback)
			}
			if err != nil {
				// Keep startup available when a bundled or customized template cannot
				// render; the missing cover is retried by the next SeedTemplates call.
				log.Printf("template %q cover generation failed: %v", id, err)
				continue
			}
			if err := s.driver.Put(context.Background(), coverKey, res.CoverPNG); err != nil {
				return err
			}
			t.CoverKey = coverKey
			b, err := json.Marshal(t)
			if err != nil {
				return err
			}
			if err := s.driver.Put(context.Background(), key, b); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) renderCover(script string) ([]byte, error) {
	res, err := s.runner.Run(script, nil)
	if err != nil {
		return nil, err
	}
	if !res.OK {
		return nil, fmt.Errorf("template render failed: %s", res.Traceback)
	}
	return res.CoverPNG, nil
}

func (s *Service) getTemplate(id string) (Template, error) {
	key, err := templateKey(id)
	if err != nil {
		return Template{}, err
	}
	b, err := s.driver.Get(context.Background(), key)
	if err != nil {
		return Template{}, err
	}
	var t Template
	if err := json.Unmarshal(b, &t); err != nil {
		return Template{}, err
	}
	if t.ID != id || t.Name == "" {
		return Template{}, fmt.Errorf("invalid template %q", id)
	}
	if t.Type == "" {
		t.Type = "pdf"
	}
	return t, nil
}

func (s *Service) listTemplates() ([]Template, error) {
	keys, err := s.driver.List(context.Background(), "templates")
	if err != nil {
		return nil, err
	}
	var out []Template
	for _, key := range keys {
		if path.Base(key) != "template.json" {
			continue
		}
		id := path.Base(path.Dir(key))
		t, err := s.getTemplate(id)
		if err != nil {
			return nil, err
		}
		t.Script = ""
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Seed creates a fixed-id document from a template and renders it. It remains
// for tests; startup now seeds template files rather than demo documents.
func (s *Service) Seed(id, templateName string) error {
	script, ok := Templates[templateName]
	if !ok {
		return fmt.Errorf("unknown template %q", templateName)
	}
	if _, err := s.store.Seed(id, script); err != nil {
		return err
	}
	res := s.run(id, 1, false)
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
	return jsonResult(map[string]any{"doc_id": doc.ID, "type": doc.Type, "version": cur.Version, "script": doc.Draft}), nil
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
	return s.run(docID, v.Version, false), nil
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

// addSvg stores an SVG asset for a document and returns its key. The script
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
	mu, _ := s.renderLocks.LoadOrStore(docID, &sync.Mutex{})
	assetLock := mu.(*sync.Mutex)
	assetLock.Lock()
	defer assetLock.Unlock()
	if _, err := s.store.Get(docID); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") || !strings.HasSuffix(strings.ToLower(name), ".svg") {
		return mcp.NewToolResultError("invalid SVG asset name"), nil
	}
	prefix, err := store.ProjectPrefix(docID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	key := path.Join(prefix, name)
	if err := s.driver.Put(context.Background(), key, []byte(svg)); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{"doc_id": docID, "name": name, "key": key}), nil
}

func (s *Service) assetsFor(docID string, version int, historical bool) (map[string][]byte, error) {
	prefix, err := store.ProjectPrefix(docID)
	if err != nil {
		return nil, err
	}
	if historical {
		vp, err := store.VersionPrefix(docID, version)
		if err != nil {
			return nil, err
		}
		prefix = path.Join(vp, "assets")
	}
	keys, err := s.driver.List(context.Background(), prefix)
	if err != nil {
		return nil, err
	}
	assets := make(map[string][]byte)
	for _, key := range keys {
		name := strings.TrimPrefix(key, prefix+"/")
		if strings.Contains(name, "/") || !isSupportedAsset(name) {
			continue
		}
		b, err := s.driver.Get(context.Background(), key)
		if err != nil {
			return nil, err
		}
		assets[name] = b
	}
	return assets, nil
}

func isSupportedAsset(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".svg", ".png", ".jpg", ".jpeg":
		return true
	default:
		return false
	}
}

// run renders a version under renderLock and publishes updating_pdf before,
// then pdf_updated or pdf_failed after. Historical rebuilds use saved assets.
func (s *Service) run(docID string, version int, historical bool) *mcp.CallToolResult {
	mu, _ := s.renderLocks.LoadOrStore(docID, &sync.Mutex{})
	renderLock := mu.(*sync.Mutex)
	renderLock.Lock()
	defer renderLock.Unlock()

	doc, err := s.store.Get(docID)
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}
	sv, ok := s.docVersion(doc, version)
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("version %d not found in document %q", version, docID))
	}

	s.hub.Publish(docID, ws.UpdatingPDF(docID))

	assets, err := s.assetsFor(docID, version, historical)
	if err != nil {
		s.hub.Publish(docID, ws.PDFFailed(docID, version, err.Error()))
		return mcp.NewToolResultError(err.Error())
	}
	res, err := s.runner.RunType(sv.Script, assets, doc.Type)
	if err != nil {
		s.hub.Publish(docID, ws.PDFFailed(docID, version, err.Error()))
		return mcp.NewToolResultError(err.Error())
	}
	if !res.OK {
		s.hub.Publish(docID, ws.PDFFailed(docID, version, res.Traceback))
		return jsonResult(map[string]any{"ok": false, "traceback": res.Traceback})
	}

	vp, err := store.VersionPrefix(docID, version)
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}
	if !historical {
		for name, data := range assets {
			if err := s.driver.Put(context.Background(), path.Join(vp, "assets", name), data); err != nil {
				s.hub.Publish(docID, ws.PDFFailed(docID, version, err.Error()))
				return mcp.NewToolResultError(err.Error())
			}
		}
	}
	pdfKey := ""
	slidesKey := ""
	if doc.Type == store.TypeSlideshow {
		slidesKey = path.Join(vp, "slides.json")
		if err := s.driver.Put(context.Background(), slidesKey, []byte(res.SlidesJSON)); err != nil {
			s.hub.Publish(docID, ws.PDFFailed(docID, version, err.Error()))
			return mcp.NewToolResultError(err.Error())
		}
	}
	if len(res.PDF) > 0 {
		pdfKey = path.Join(vp, "pdf_render.pdf")
		if err := s.driver.Put(context.Background(), pdfKey, res.PDF); err != nil {
			s.hub.Publish(docID, ws.PDFFailed(docID, version, err.Error()))
			return mcp.NewToolResultError(err.Error())
		}
	}
	coverKey := path.Join(vp, "cover.png")
	if err := s.driver.Put(context.Background(), coverKey, res.CoverPNG); err != nil {
		s.hub.Publish(docID, ws.PDFFailed(docID, version, err.Error()))
		return mcp.NewToolResultError(err.Error())
	}
	pptxKey := ""
	if doc.Type == "slideshow" {
		pptxKey = path.Join(vp, "slides.pptx")
		if err := s.driver.Put(context.Background(), pptxKey, res.PPTX); err != nil {
			s.hub.Publish(docID, ws.PDFFailed(docID, version, err.Error()))
			return mcp.NewToolResultError(err.Error())
		}
	}
	if err := s.store.RecordOutput(docID, version, store.Output{PDFKey: pdfKey, CoverKey: coverKey, PPTXKey: pptxKey, SlidesKey: slidesKey, Manifest: res.Manifest}); err != nil {
		s.hub.Publish(docID, ws.PDFFailed(docID, version, err.Error()))
		return mcp.NewToolResultError(err.Error())
	}
	pdfURL := ""
	if pdfKey != "" {
		pdfURL = fmt.Sprintf("/docs/%s/pdf?version=%d", docID, version)
	}
	updated := ws.PDFUpdated(docID, version,
		pdfURL,
		fmt.Sprintf("/docs/%s/manifest?version=%d", docID, version))
	updated["project_type"] = store.NormalizeType(doc.Type)
	if pptxKey != "" {
		updated["pptx_url"] = fmt.Sprintf("/docs/%s/pptx?version=%d", docID, version)
		updated["slides_url"] = fmt.Sprintf("/docs/%s/slides?version=%d", docID, version)
	}
	s.hub.Publish(docID, updated)
	return jsonResult(map[string]any{
		"ok":         true,
		"version":    version,
		"pdf_key":    pdfKey,
		"cover_key":  coverKey,
		"pptx_key":   pptxKey,
		"slides_key": slidesKey,
		"manifest":   json.RawMessage(res.Manifest),
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
			"add_svg", "Store an SVG asset for a document and return its key; reference it in the script with doc.svg(name, element_id).",
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
	s.RegisterSourceTools(mcpSrv)
}
