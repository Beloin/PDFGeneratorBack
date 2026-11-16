package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"pdfeditor/server/internal/store"
)

func multipartRequest(t *testing.T, svc *Service, target, filename, contentType string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	svc.RegisterUploadRoutes(mux)
	req := httptest.NewRequest("POST", target, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if contentType != "" {
		req.Header.Set("X-Ignored-Test-Type", contentType)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestPDFSourceExtraction(t *testing.T) {
	// A small one-page PDF with a text operator, accepted by pdftotext.
	pdf, decodeErr := base64.StdEncoding.DecodeString("JVBERi0xLjMKJZOMi54gUmVwb3J0TGFiIEdlbmVyYXRlZCBQREYgZG9jdW1lbnQgKG9wZW5zb3VyY2UpCjEgMCBvYmoKPDwKL0YxIDIgMCBSCj4+CmVuZG9iagoyIDAgb2JqCjw8Ci9CYXNlRm9udCAvSGVsdmV0aWNhIC9FbmNvZGluZyAvV2luQW5zaUVuY29kaW5nIC9OYW1lIC9GMSAvU3VidHlwZSAvVHlwZTEgL1R5cGUgL0ZvbnQKPj4KZW5kb2JqCjMgMCBvYmoKPDwKL0NvbnRlbnRzIDcgMCBSIC9NZWRpYUJveCBbIDAgMCA1OTUuMjc1NiA4NDEuODg5OCBdIC9QYXJlbnQgNiAwIFIgL1Jlc291cmNlcyA8PAovRm9udCAxIDAgUiAvUHJvY1NldCBbIC9QREYgL1RleHQgL0ltYWdlQiAvSW1hZ2VDIC9JbWFnZUkgXQo+PiAvUm90YXRlIDAgL1RyYW5zIDw8Cgo+PiAKICAvVHlwZSAvUGFnZQo+PgplbmRvYmoKNCAwIG9iago8PAovUGFnZU1vZGUgL1VzZU5vbmUgL1BhZ2VzIDYgMCBSIC9UeXBlIC9DYXRhbG9nCj4+CmVuZG9iago1IDAgb2JqCjw8Ci9BdXRob3IgKGFub255bW91cykgL0NyZWF0aW9uRGF0ZSAoRDoyMDI2MTExNTE2MTMwNy0wMycwMCcpIC9DcmVhdG9yIChhbm9ueW1vdXMpIC9LZXl3b3JkcyAoKSAvTW9kRGF0ZSAoRDoyMDI2MTExNTE2MTMwNy0wMycwMCcpIC9Qcm9kdWNlciAoUmVwb3J0TGFiIFBERiBMaWJyYXJ5IC0gXChvcGVuc291cmNlXCkpIAogIC9TdWJqZWN0ICh1bnNwZWNpZmllZCkgL1RpdGxlICh1bnRpdGxlZCkgL1RyYXBwZWQgL0ZhbHNlCj4+CmVuZG9iago2IDAgb2JqCjw8Ci9Db3VudCAxIC9LaWRzIFsgMyAwIFIgXSAvVHlwZSAvUGFnZXMKPj4KZW5kb2JqCjcgMCBvYmoKPDwKL0ZpbHRlciBbIC9BU0NJSTg1RGVjb2RlIC9GbGF0ZURlY29kZSBdIC9MZW5ndGggMTE0Cj4+CnN0cmVhbQpHYXBRaDBFPUYsMFVcSDNUXHBOWVReUUtrP3RjPklQLDtXI1UxXjIzaWhQRU1fP0NXNEtJU2k3OmJYZ146XkcwQ2NqN29NL1A4YzwtcVVOaDsrU20zLW4tYD9lUGxncjtAKFlPa3UkQD8hNkszcj9Ofj5lbmRzdHJlYW0KZW5kb2JqCnhyZWYKMCA4CjAwMDAwMDAwMDAgNjU1MzUgZiAKMDAwMDAwMDA2MSAwMDAwMCBuIAowMDAwMDAwMDkyIDAwMDAwIG4gCjAwMDAwMDAxOTkgMDAwMDAgbiAKMDAwMDAwMDQwMiAwMDAwMCBuIAowMDAwMDAwNDcwIDAwMDAwIG4gCjAwMDAwMDA3MzEgMDAwMDAgbiAKMDAwMDAwMDc5MCAwMDAwMCBuIAp0cmFpbGVyCjw8Ci9JRCAKWzw1NDBhNTliMTU5YjlhYzYwYTcwODcwYWFmZmJiMjkxMz48NTQwYTU5YjE1OWI5YWM2MGE3MDg3MGFhZmZiYjI5MTM+XQolIFJlcG9ydExhYiBnZW5lcmF0ZWQgUERGIGRvY3VtZW50IC0tIGRpZ2VzdCAob3BlbnNvdXJjZSkKCi9JbmZvIDUgMCBSCi9Sb290IDQgMCBSCi9TaXplIDgKPj4Kc3RhcnR4cmVmCjk5NAolJUVPRgo=")
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	got, err := pdfToMarkdown(pdf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Source text fixture") {
		t.Fatalf("extracted text: %q", got)
	}
	blankPDF, err := base64.StdEncoding.DecodeString("JVBERi0xLjMKJZOMi54gUmVwb3J0TGFiIEdlbmVyYXRlZCBQREYgZG9jdW1lbnQgKG9wZW5zb3VyY2UpCjEgMCBvYmoKPDwKL0YxIDIgMCBSCj4+CmVuZG9iagoyIDAgb2JqCjw8Ci9CYXNlRm9udCAvSGVsdmV0aWNhIC9FbmNvZGluZyAvV2luQW5zaUVuY29kaW5nIC9OYW1lIC9GMSAvU3VidHlwZSAvVHlwZTEgL1R5cGUgL0ZvbnQKPj4KZW5kb2JqCjMgMCBvYmoKPDwKL0NvbnRlbnRzIDcgMCBSIC9NZWRpYUJveCBbIDAgMCA1OTUuMjc1NiA4NDEuODg5OCBdIC9QYXJlbnQgNiAwIFIgL1Jlc291cmNlcyA8PAovRm9udCAxIDAgUiAvUHJvY1NldCBbIC9QREYgL1RleHQgL0ltYWdlQiAvSW1hZ2VDIC9JbWFnZUkgXQo+PiAvUm90YXRlIDAgL1RyYW5zIDw8Cgo+PiAKICAvVHlwZSAvUGFnZQo+PgplbmRvYmoKNCAwIG9iago8PAovUGFnZU1vZGUgL1VzZU5vbmUgL1BhZ2VzIDYgMCBSIC9UeXBlIC9DYXRhbG9nCj4+CmVuZG9iago1IDAgb2JqCjw8Ci9BdXRob3IgKGFub255bW91cykgL0NyZWF0aW9uRGF0ZSAoRDoyMDI2MTExNTE2MTQzNi0wMycwMCcpIC9DcmVhdG9yIChhbm9ueW1vdXMpIC9LZXl3b3JkcyAoKSAvTW9kRGF0ZSAoRDoyMDI2MTExNTE2MTQzNi0wMycwMCcpIC9Qcm9kdWNlciAoUmVwb3J0TGFiIFBERiBMaWJyYXJ5IC0gXChvcGVuc291cmNlXCkpIAogIC9TdWJqZWN0ICh1bnNwZWNpZmllZCkgL1RpdGxlICh1bnRpdGxlZCkgL1RyYXBwZWQgL0ZhbHNlCj4+CmVuZG9iago2IDAgb2JqCjw8Ci9Db3VudCAxIC9LaWRzIFsgMyAwIFIgXSAvVHlwZSAvUGFnZXMKPj4KZW5kb2JqCjcgMCBvYmoKPDwKL0ZpbHRlciBbIC9BU0NJSTg1RGVjb2RlIC9GbGF0ZURlY29kZSBdIC9MZW5ndGggNTkKPj4Kc3RyZWFtCkdhcFFoMEU9RiwwVVxIM1RccE5ZVF5RS2s/dGM+SVAsO1cjVTFeMjNpaFBFTV9QUCRPITNeLEM1UX4+ZW5kc3RyZWFtCmVuZG9iagp4cmVmCjAgOAowMDAwMDAwMDAwIDY1NTM1IGYgCjAwMDAwMDAwNjEgMDAwMDAgbiAKMDAwMDAwMDA5MiAwMDAwMCBuIAowMDAwMDAwMTk5IDAwMDAwIG4gCjAwMDAwMDA0MDIgMDAwMDAgbiAKMDAwMDAwMDQ3MCAwMDAwMCBuIAowMDAwMDAwNzMxIDAwMDAwIG4gCjAwMDAwMDA3OTAgMDAwMDAgbiAKdHJhaWxlcgo8PAovSUQgCls8OTkyYjJhZjUxOWIzNTIxMDBiZWI5ZTk2NGJmNWU1NGU+PDk5MmIyYWY1MTliMzUyMTAwYmViOWU5NjRiZjVlNTRlPl0KJSBSZXBvcnRMYWIgZ2VuZXJhdGVkIFBERiBkb2N1bWVudCAtLSBkaWdlc3QgKG9wZW5zb3VyY2UpCgovSW5mbyA1IDAgUgovUm9vdCA0IDAgUgovU2l6ZSA4Cj4+CnN0YXJ0eHJlZgo5MzgKJSVFT0YK")
	if err != nil {
		t.Fatal(err)
	}
	if got, err = pdfToMarkdown(blankPDF); err == nil || !strings.Contains(err.Error(), "no extractable text") {
		t.Fatalf("scan-only PDF error: %q, %v", got, err)
	}
}

func TestSourceMarkdownUploadListAndRead(t *testing.T) {
	svc, driver, st := testService(t, validRender)
	doc, err := st.CreateProject("", "", "sources", store.TypePDF)
	if err != nil {
		t.Fatal(err)
	}
	w := multipartRequest(t, svc, "/docs/"+doc.ID+"/sources", "brief.md", "", []byte("# Brief\nKeep this text."))
	if w.Code != 201 {
		t.Fatalf("upload status %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.ID == "" {
		t.Fatalf("upload result: %s (%v)", w.Body.String(), err)
	}
	got, err := driver.Get(t.Context(), result.Key)
	if err != nil || string(got) != "# Brief\nKeep this text." {
		t.Fatalf("stored source %q: %v", got, err)
	}
	mux := http.NewServeMux()
	svc.RegisterUploadRoutes(mux)
	list := httptest.NewRecorder()
	mux.ServeHTTP(list, httptest.NewRequest("GET", "/docs/"+doc.ID+"/sources", nil))
	if list.Code != 200 || !strings.Contains(list.Body.String(), result.ID) || !strings.Contains(list.Body.String(), "brief.md") {
		t.Fatalf("list %d: %s", list.Code, list.Body.String())
	}
	read := httptest.NewRecorder()
	mux.ServeHTTP(read, httptest.NewRequest("GET", "/docs/"+doc.ID+"/sources/"+result.ID, nil))
	if read.Code != 200 || read.Body.String() != "# Brief\nKeep this text." {
		t.Fatalf("read %d: %s", read.Code, read.Body.String())
	}
}

func TestSourceToolsListNamesAndEnforceDocumentScope(t *testing.T) {
	svc, _, st := testService(t, validRender)
	doc1, err := st.CreateProject("", "", "one", store.TypePDF)
	if err != nil {
		t.Fatal(err)
	}
	doc2, err := st.CreateProject("", "", "two", store.TypePDF)
	if err != nil {
		t.Fatal(err)
	}
	w := multipartRequest(t, svc, "/docs/"+doc1.ID+"/sources", "notes.md", "", []byte("private source"))
	if w.Code != 201 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	var up struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &up); err != nil {
		t.Fatal(err)
	}
	srv := server.NewMCPServer("test", "1")
	svc.RegisterSourceTools(srv)
	args := func(docID, sourceID string) mcp.CallToolRequest {
		return mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"doc_id": docID, "source_id": sourceID}}}
	}
	get := srv.GetTool("get_source")
	res, err := get.Handler(context.Background(), args(doc1.ID, up.ID))
	if err != nil || res.IsError {
		t.Fatalf("same document source: result=%v err=%v", res, err)
	}
	res, err = get.Handler(context.Background(), args(doc2.ID, up.ID))
	if err != nil || !res.IsError {
		t.Fatalf("cross-document source should fail: result=%v err=%v", res, err)
	}
	res, err = get.Handler(context.Background(), args(doc1.ID, "missing"))
	if err != nil || !res.IsError {
		t.Fatalf("missing source should fail: result=%v err=%v", res, err)
	}
	list := srv.GetTool("list_sources")
	res, err = list.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"doc_id": doc1.ID}}})
	if err != nil || res.IsError || !strings.Contains(resultText(res), "notes.md") {
		t.Fatalf("list should preserve original name: result=%v err=%v", res, err)
	}
}

func TestImageUploadAndRead(t *testing.T) {
	svc, driver, st := testService(t, validRender)
	doc, err := st.CreateProject("", "", "image", store.TypePDF)
	if err != nil {
		t.Fatal(err)
	}
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC")
	if err != nil {
		t.Fatal(err)
	}
	w := multipartRequest(t, svc, "/docs/"+doc.ID+"/images", "photo.png", "", png)
	if w.Code != 201 {
		t.Fatalf("upload %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Name string `json:"name"`
		Key  string `json:"key"`
		URL  string `json:"url"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Name == "" || result.Key == "" || result.URL != "/docs/"+doc.ID+"/images/"+result.Name {
		t.Fatalf("image response: %s", w.Body.String())
	}
	mux := http.NewServeMux()
	svc.RegisterUploadRoutes(mux)
	read := httptest.NewRecorder()
	mux.ServeHTTP(read, httptest.NewRequest("GET", result.URL, nil))
	stored, e := driver.Get(t.Context(), result.Key)
	if e != nil || read.Code != 200 || !bytes.Equal(read.Body.Bytes(), stored) {
		t.Fatalf("image read status %d: %v", read.Code, e)
	}
}

func TestUploadsRejectUnknownDocumentAndUnsupportedFiles(t *testing.T) {
	svc, _, _ := testService(t, validRender)
	if w := multipartRequest(t, svc, "/docs/missing/sources", "x.md", "", []byte("x")); w.Code != 404 {
		t.Fatalf("unknown doc source: %d", w.Code)
	}
	doc, err := svc.store.CreateProject("", "", "test", store.TypePDF)
	if err != nil {
		t.Fatal(err)
	}
	if w := multipartRequest(t, svc, "/docs/"+doc.ID+"/images", "x.gif", "", []byte("GIF89a")); w.Code != 415 {
		t.Fatalf("unsupported image: %d %s", w.Code, w.Body.String())
	}
	if w := multipartRequest(t, svc, "/docs/"+doc.ID+"/sources", "x.exe", "", []byte("bad")); w.Code != 415 {
		t.Fatalf("unsupported source: %d %s", w.Code, w.Body.String())
	}
}
