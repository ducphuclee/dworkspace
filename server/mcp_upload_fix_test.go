package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPUploadFileAndAssetWorkspaceFix(t *testing.T) {
	s := testServer(t)
	uid, cookie := signedIn(t, s, "mcp-upload-fix@example.test")
	u := &user{ID: uid, Name: "Test"}
	ws := s.firstWorkspaceOf(t, uid)
	page := s.makePage(t, ws, uid, "", "Uploads", "{}")
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j5n8AAAAASUVORK5CYII="

	out, err := s.mcpCall(u, "upload_file", json.RawMessage(`{"page_id":"`+page+`","file_name":"pixel.png","data_base64":"`+png+`"}`), "")
	if err != nil {
		t.Fatalf("upload_file: %v", err)
	}
	if !strings.HasPrefix(out, "Uploaded") {
		t.Fatalf("upload_file result = %q, want prefix %q", out, "Uploaded")
	}

	pageOut, err := s.mcpCall(u, "get_page", json.RawMessage(`{"page_id":"`+page+`"}`), "")
	if err != nil {
		t.Fatalf("get_page: %v", err)
	}
	if !strings.Contains(pageOut, "![pixel.png](/files/") {
		t.Fatalf("get_page does not contain an image block pointing to /files/: %s", pageOut)
	}

	var upload struct {
		AssetID string `json:"assetId"`
		URL     string `json:"url"`
		Name    string `json:"name"`
	}
	assetOut, err := s.mcpCall(u, "upload_asset", json.RawMessage(`{"file_name":"asset.png","data_base64":"`+png+`"}`), "")
	if err != nil {
		t.Fatalf("upload_asset: %v", err)
	}
	if err := json.Unmarshal([]byte(assetOut), &upload); err != nil {
		t.Fatalf("upload_asset result is not JSON: %v; result=%q", err, assetOut)
	}
	if upload.AssetID == "" || upload.URL == "" || upload.Name != "asset.png" {
		t.Fatalf("upload_asset result has wrong shape: %+v", upload)
	}

	// The uploaded image block on the page must make its stored asset eligible
	// for the direct get_asset fast path.
	// upload_file returns its URL; extract the stored filename from it.
	url := out[strings.LastIndex(out, "/files/")+len("/files/"):]
	if url == out || !assetReferencedByPage(s, page, url) {
		t.Fatalf("asset %q is not referenced by page %q", url, page)
	}
	imageAssetID := url

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "get_asset",
			"arguments": map[string]string{"page_id": page, "asset_id": imageAssetID},
		},
	})
	if err != nil {
		t.Fatalf("marshal get_asset request: %v", err)
	}
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("POST /mcp get_asset status=%d body=%s", rec.Code, rec.Body.String())
	}
	var rpc struct {
		Result struct {
			Content []struct {
				Type     string `json:"type"`
				Data     string `json:"data"`
				MIMEType string `json:"mimeType"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rpc); err != nil {
		t.Fatalf("decode get_asset response: %v; body=%s", err, rec.Body.String())
	}
	if rpc.Result.IsError || len(rpc.Result.Content) != 1 || rpc.Result.Content[0].Type != "image" || rpc.Result.Content[0].Data != png || !strings.HasPrefix(rpc.Result.Content[0].MIMEType, "image/") {
		t.Fatalf("get_asset did not return the uploaded image: %+v", rpc.Result)
	}
}
