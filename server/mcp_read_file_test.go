package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateReadFileURL_rejects_noncanonical_and_traversal_references(t *testing.T) {
	// Given
	tests := []string{
		"",
		"https://example.com/file.png",
		"/files/",
		"/files/../secret",
		"/files/%2e%2e/secret",
		"/files/%2Fetc%2Fpasswd",
		"/files/name/other",
		"/files/name?download=1",
		"/files/name#fragment",
		"/files/name\\other",
	}

	// When / Then
	for _, reference := range tests {
		if _, err := validateReadFileURL(reference); err == nil {
			t.Errorf("reference %q was accepted", reference)
		}
	}
}

func TestValidateReadFileURL_accepts_one_canonical_segment(t *testing.T) {
	// Given
	reference := "/files/01abc-def_ghi.png"

	// When
	name, err := validateReadFileURL(reference)

	// Then
	if err != nil {
		t.Fatalf("validateReadFileURL: %v", err)
	}
	if name != "01abc-def_ghi.png" {
		t.Fatalf("stored name = %q", name)
	}
}

func TestDetectReadFileMIME_recognizes_native_images_and_rejects_fake_content(t *testing.T) {
	// Given
	png := mustDecodeBase64(t, "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	jpeg := mustDecodeBase64(t, "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAP//////////////////////////////////////////////////////////////////////////////////////2wBDAf//////////////////////////////////////////////////////////////////////////////////////wAARCAABAAEDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAX/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIQAxAAAAH/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/9oACAEBAAEFAqf/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oACAEDAQE/AR//xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oACAECAQE/AR//xAAUEAEAAAAAAAAAAAAAAAAAAAAA/9oACAEBAAY/Aqf/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/9oACAEBAAE/IV//2gAMAwEAAgADAAAAEP/EABQRAQAAAAAAAAAAAAAAAAAAABD/2gAIAQMBAT8QH//EABQRAQAAAAAAAAAAAAAAAAAAABD/2gAIAQIBAT8QH//EABQQAQAAAAAAAAAAAAAAAAAAABD/2gAIAQEAAT8QH//Z")
	gif := mustDecodeBase64(t, "R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==")
	webp := mustDecodeBase64(t, "UklGRjwAAABXRUJQVlA4IDAAAADQAQCdASoBAAEAAUAmJaACdLoB+AADsAD+8ut//NgVzXPv9//S4P0uD9Lg/9KQAAA=")

	// When / Then
	for _, test := range []struct {
		name string
		data []byte
		want string
	}{
		{name: "png", data: png, want: "image/png"},
		{name: "jpeg", data: jpeg, want: "image/jpeg"},
		{name: "gif", data: gif, want: "image/gif"},
		{name: "webp", data: webp, want: "image/webp"},
	} {
		if got := detectReadFileMIME(test.data); got != test.want {
			t.Errorf("%s MIME = %q, want %q", test.name, got, test.want)
		}
	}
	if got := detectReadFileMIME([]byte("not really a PNG")); got == "image/png" {
		t.Errorf("fake image detected as %q", got)
	}
	malformedWebP := webp[:len(webp)-1]
	if got := detectReadFileMIME(malformedWebP); got == "image/webp" {
		t.Errorf("malformed WebP detected as %q", got)
	}
}

func TestDetectReadFileMIME_recognizes_svg_as_metadata_only(t *testing.T) {
	// Given
	data := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`)

	// When
	got := detectReadFileMIME(data)

	// Then
	if got != "image/svg+xml" {
		t.Fatalf("SVG MIME = %q", got)
	}
}

func TestMCPReadFile_returnsNativeImageContentOverWire(t *testing.T) {
	// Given
	s := testServer(t)
	uid, cookie := signedIn(t, s, "read-file-image@example.test")
	ws := s.firstWorkspaceOf(t, uid)
	if err := os.WriteFile(filepath.Join(s.dataDir, "files", "native.png"), mustDecodeBase64(t, "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="), 0o644); err != nil {
		t.Fatalf("write PNG fixture: %v", err)
	}
	seedPage(t, s, "image-page", "", ws, uid, "workspace", "[]")
	s.recordFile("native.png", "image-page", "Native.png")

	// When
	response := mcpWireCall(t, s, cookie, "read_file", `{"url":"/files/native.png"}`)

	// Then
	result := responseResult(t, response)
	var content []struct {
		Type     string `json:"type"`
		Data     string `json:"data"`
		MIMEType string `json:"mimeType"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal(result["content"], &content); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if len(content) != 1 || content[0].Type != "image" || content[0].MIMEType != "image/png" || content[0].Text != "" {
		t.Fatalf("wire content = %+v", content)
	}
	data, err := base64.StdEncoding.DecodeString(content[0].Data)
	if err != nil {
		t.Fatalf("decode image content: %v", err)
	}
	if len(data) == 0 || !bytes.HasPrefix(data, []byte("\x89PNG")) {
		t.Fatalf("native image data is not PNG: %x", data[:min(8, len(data))])
	}

	metadataResponse := mcpWireCall(t, s, cookie, "read_file", `{"url":"/files/native.png","representation":"metadata"}`)
	metadataResult := responseResult(t, metadataResponse)
	if strings.Contains(string(metadataResult["content"]), base64.StdEncoding.EncodeToString(data)) {
		t.Fatal("metadata response inlined image bytes")
	}
}

func TestMCPReadFile_catalogueUsesPinnedMCPFieldCasing(t *testing.T) {
	// Given
	var tool map[string]any
	for _, candidate := range mcpTools {
		if candidate["name"] == "read_file" {
			tool = candidate
			break
		}
	}
	if tool == nil {
		t.Fatal("read_file is missing from the MCP catalogue")
	}

	// When / Then
	if _, ok := tool["inputSchema"]; !ok {
		t.Fatal("read_file schema must use MCP's inputSchema field")
	}
	schema, ok := tool["inputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("inputSchema type = %T", tool["inputSchema"])
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T", schema["properties"])
	}
	for _, field := range []string{"url", "max_bytes", "representation"} {
		if _, ok := properties[field]; !ok {
			t.Errorf("read_file is missing %q property", field)
		}
	}
}

func TestMCPReadFile_enforcesPagePermissionsAndDeniesOrphans(t *testing.T) {
	// Given
	s := testServer(t)
	admin, _ := signedIn(t, s, "read-file-admin@example.test")
	ws := s.firstWorkspaceOf(t, admin)
	alice, aliceCookie := signedIn(t, s, "read-file-alice@example.test")
	bob, bobCookie := signedIn(t, s, "read-file-bob@example.test")
	addWorkspaceMember(t, s, workspaceMember{ws, alice})
	addWorkspaceMember(t, s, workspaceMember{ws, bob})
	seedFile(t, s, "private.png", "not an image")
	seedPage(t, s, "private-page", "", ws, alice, "private", "[]")
	s.recordFile("private.png", "private-page", "Private.png")
	seedFile(t, s, "orphan.png", "not an image")
	s.recordFile("orphan.png", "", "Orphan.png")

	// When / Then
	unauthorized := responseErrorCode(t, mcpWireCall(t, s, bobCookie, "read_file", `{"url":"/files/private.png"}`))
	missing := responseErrorCode(t, mcpWireCall(t, s, bobCookie, "read_file", `{"url":"/files/missing.png"}`))
	orphan := responseErrorCode(t, mcpWireCall(t, s, aliceCookie, "read_file", `{"url":"/files/orphan.png"}`))
	if unauthorized != "file_not_found" || missing != unauthorized || orphan != unauthorized {
		t.Fatalf("file visibility errors = unauthorized %q, missing %q, orphan %q", unauthorized, missing, orphan)
	}
}

func TestMCPReadFile_returnsMachineReadableValidationAndRepresentationErrors(t *testing.T) {
	// Given
	s := testServer(t)
	uid, cookie := signedIn(t, s, "read-file-errors@example.test")
	ws := s.firstWorkspaceOf(t, uid)
	seedPage(t, s, "read-file-errors-page", "", ws, uid, "workspace", "[]")
	seedFile(t, s, "fake.png", "not an image")
	s.recordFile("fake.png", "read-file-errors-page", "Fake.png")
	seedFile(t, s, "truncated.bin", "\x89PNG\r\n\x1a\n")
	s.recordFile("truncated.bin", "read-file-errors-page", "Truncated.bin")
	if err := os.Mkdir(filepath.Join(s.dataDir, "files", "directory.bin"), 0o755); err != nil {
		t.Fatalf("create directory fixture: %v", err)
	}
	s.recordFile("directory.bin", "read-file-errors-page", "Directory.bin")
	seedFile(t, s, "vector.svg", `<svg xmlns="http://www.w3.org/2000/svg"/>`)
	s.recordFile("vector.svg", "read-file-errors-page", "Vector.svg")

	// When / Then
	if got := responseErrorCode(t, mcpWireCall(t, s, cookie, "read_file", `{"url":"https://example.com/x.png"}`)); got != "invalid_file_reference" {
		t.Errorf("external URL code = %q", got)
	}
	if got := responseErrorCode(t, mcpWireCall(t, s, cookie, "read_file", `{"url":"/files/fake.png"}`)); got != "invalid_image_content" {
		t.Errorf("fake image code = %q", got)
	}
	if got := responseErrorCode(t, mcpWireCall(t, s, cookie, "read_file", `{"url":"/files/truncated.bin"}`)); got != "invalid_image_content" {
		t.Errorf("truncated image code = %q", got)
	}
	if got := responseErrorCode(t, mcpWireCall(t, s, cookie, "read_file", `{"url":"/files/directory.bin"}`)); got != "read_failed" {
		t.Errorf("directory read code = %q", got)
	}
	if got := responseErrorCode(t, mcpWireCall(t, s, cookie, "read_file", `{"url":"/files/vector.svg","representation":"image"}`)); got != "unsupported_representation" {
		t.Errorf("SVG image code = %q", got)
	}
	result := responseResult(t, mcpWireCall(t, s, cookie, "read_file", `{"url":"/files/vector.svg"}`))
	var content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result["content"], &content); err != nil {
		t.Fatalf("decode SVG metadata content: %v", err)
	}
	if len(content) != 1 || content[0].Type != "text" || !strings.Contains(content[0].Text, `"mime_type":"image/svg+xml"`) {
		t.Fatalf("SVG metadata = %+v", content)
	}
}

func TestMCPReadFile_appliesSeparateImageReadLimit(t *testing.T) {
	// Given
	s := testServer(t)
	uid, cookie := signedIn(t, s, "read-file-limit@example.test")
	ws := s.firstWorkspaceOf(t, uid)
	seedPage(t, s, "read-file-limit-page", "", ws, uid, "workspace", "[]")
	path := filepath.Join(s.dataDir, "files", "large.png")
	data := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{'x'}, 1<<20)...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write large fixture: %v", err)
	}
	s.recordFile("large.png", "read-file-limit-page", "Large.png")
	s.setSetting("max_image_read_mb", "1")

	// When / Then
	if got := responseErrorCode(t, mcpWireCall(t, s, cookie, "read_file", `{"url":"/files/large.png","max_bytes":100}`)); got != "image_too_large" {
		t.Fatalf("image limit code = %q", got)
	}
}

func TestMaxImageReadBytes_defaultsToTenMBAndUsesSeparateSetting(t *testing.T) {
	// Given
	s := testServer(t)

	// When / Then
	if got := s.maxImageReadBytes(); got != 10<<20 {
		t.Fatalf("default image-read limit = %d, want %d", got, 10<<20)
	}
	s.setSetting("max_upload_mb", "50")
	s.setSetting("max_image_read_mb", "12")
	if got := s.maxImageReadBytes(); got != 12<<20 {
		t.Fatalf("configured image-read limit = %d, want %d", got, 12<<20)
	}
}

func TestMCPReadFile_preservesUploadPageAndFileIndexFlows(t *testing.T) {
	// Given
	s := testServer(t)
	uid, _ := signedIn(t, s, "read-file-regression@example.test")
	ws := s.firstWorkspaceOf(t, uid)
	seedPage(t, s, "read-file-regression-page", "", ws, uid, "workspace", "[]")
	u := s.userByID(uid)

	// When
	if _, err := s.mcpCall(u, "upload_file", json.RawMessage(`{"page_id":"read-file-regression-page","file_name":"notes.txt","data_base64":"aGVsbG8="}`), ""); err != nil {
		t.Fatalf("upload_file: %v", err)
	}
	page, err := s.mcpCall(u, "get_page", json.RawMessage(`{"page_id":"read-file-regression-page"}`), "")
	if err != nil {
		t.Fatalf("get_page: %v", err)
	}
	files, err := s.mcpListFiles(u, ws, "")
	if err != nil {
		t.Fatalf("list files: %v", err)
	}

	// Then
	if !strings.Contains(page, "/files/") || !strings.Contains(page, "notes.txt") {
		t.Fatalf("get_page lost uploaded file reference: %s", page)
	}
	if !strings.Contains(files, `"name":"notes.txt"`) || !strings.Contains(files, `"count":1`) {
		t.Fatalf("list(files) lost uploaded file: %s", files)
	}
}

func mcpWireCall(t *testing.T, s *Server, cookie, tool, arguments string) map[string]json.RawMessage {
	t.Helper()
	rec := httptest.NewRecorder()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + arguments + `}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/json")
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("MCP wire status = %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode MCP wire response: %v", err)
	}
	return response
}

func responseResult(t *testing.T, response map[string]json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var result map[string]json.RawMessage
	if err := json.Unmarshal(response["result"], &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return result
}

func responseErrorCode(t *testing.T, response map[string]json.RawMessage) string {
	t.Helper()
	result := responseResult(t, response)
	var content []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result["content"], &content); err != nil {
		t.Fatalf("decode error content: %v", err)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if len(content) != 1 || json.Unmarshal([]byte(content[0].Text), &body) != nil {
		t.Fatalf("error content is not machine-readable: %+v", content)
	}
	return body.Error.Code
}

func mustDecodeBase64(t *testing.T, value string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return b
}
