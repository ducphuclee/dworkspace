package server

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const readFileSampleBytes = 512

type mcpReadError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *mcpReadError) Error() string { return e.Code + ": " + e.Message }

type mcpReadFileResult struct {
	Metadata map[string]any
	MIME     string
	Data     []byte
}

func (s *Server) mcpToolResult(u *user, name string, rawArgs json.RawMessage, publicBase string) map[string]any {
	if name != "read_file" {
		result, err := s.mcpCall(u, name, rawArgs, publicBase)
		if err != nil {
			return textResult(err.Error(), true)
		}
		return textResult(result, false)
	}
	result, err := s.mcpReadFile(u, rawArgs)
	if err != nil {
		return mcpReadErrorContent(err)
	}
	if len(result.Data) > 0 {
		return result.content()
	}
	text, err := result.text()
	if err != nil {
		return mcpReadErrorContent(err)
	}
	return textResult(text, false)
}

func (r mcpReadFileResult) text() (string, error) {
	b, err := json.Marshal(r.Metadata)
	if err != nil {
		return "", fmt.Errorf("marshal file metadata: %w", err)
	}
	return string(b), nil
}

func (r mcpReadFileResult) content() map[string]any {
	return map[string]any{
		"content": []map[string]any{{
			"type":     "image",
			"data":     base64.StdEncoding.EncodeToString(r.Data),
			"mimeType": r.MIME,
		}},
		"isError": false,
	}
}

func mcpReadErrorContent(err error) map[string]any {
	var readErr *mcpReadError
	if !errors.As(err, &readErr) {
		return textResult(err.Error(), true)
	}
	b, marshalErr := json.Marshal(map[string]any{"error": readErr})
	if marshalErr != nil {
		return textResult(readErr.Error(), true)
	}
	return textResult(string(b), true)
}

func validateReadFileURL(reference string) (string, error) {
	if !strings.HasPrefix(reference, "/files/") {
		return "", &mcpReadError{Code: "invalid_file_reference", Message: "url must be /files/<single-segment>"}
	}
	name := strings.TrimPrefix(reference, "/files/")
	if name == "" || strings.ContainsAny(name, `/\\?#%`) || name == "." || name == ".." || strings.IndexByte(name, 0) >= 0 {
		return "", &mcpReadError{Code: "invalid_file_reference", Message: "url must be /files/<single-segment>"}
	}
	return name, nil
}

func detectReadFileMIME(data []byte) string {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && (bytes.HasPrefix(trimmed, []byte("<svg")) || bytes.Contains(trimmed, []byte("<svg ")) || bytes.Contains(trimmed, []byte("<svg>"))) {
		return "image/svg+xml"
	}
	if _, format, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		switch format {
		case "png":
			return "image/png"
		case "jpeg":
			return "image/jpeg"
		case "gif":
			return "image/gif"
		}
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		if validateWebP(data) {
			return "image/webp"
		}
		return "application/octet-stream"
	}
	return http.DetectContentType(data)
}

func validateWebP(data []byte) bool {
	if len(data) < 20 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return false
	}
	formEnd := int(binary.LittleEndian.Uint32(data[4:8])) + 8
	if formEnd != len(data) || formEnd < 20 {
		return false
	}
	foundImage := false
	pos := 12
	for pos+8 <= formEnd {
		chunkEnd := pos + 8 + int(binary.LittleEndian.Uint32(data[pos+4:pos+8]))
		if chunkEnd > formEnd {
			return false
		}
		payload := data[pos+8 : chunkEnd]
		switch string(data[pos : pos+4]) {
		case "VP8 ":
			if len(payload) >= 10 && payload[0]&1 == 0 && bytes.Equal(payload[3:6], []byte{0x9d, 0x01, 0x2a}) {
				width := int(binary.LittleEndian.Uint16(payload[6:8]) & 0x3fff)
				height := int(binary.LittleEndian.Uint16(payload[8:10]) & 0x3fff)
				foundImage = width > 0 && height > 0
			}
		case "VP8L":
			if len(payload) >= 5 && payload[0] == 0x2f {
				width := 1 + int(payload[1]) + (int(payload[2]&0x3f) << 8)
				height := 1 + int(payload[2]>>6) + (int(payload[3]) << 2) + (int(payload[4]&0x0f) << 10)
				foundImage = width > 0 && height > 0
			}
		case "VP8X":
			if len(payload) < 10 {
				return false
			}
		}
		pos = chunkEnd
		if pos%2 != 0 {
			pos++
		}
	}
	return pos == formEnd && foundImage
}

func (s *Server) mcpReadFile(u *user, rawArgs json.RawMessage) (mcpReadFileResult, error) {
	var args struct {
		URL            string `json:"url"`
		MaxBytes       *int64 `json:"max_bytes"`
		Representation string `json:"representation"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return mcpReadFileResult{}, &mcpReadError{Code: "invalid_file_reference", Message: "invalid read_file arguments"}
	}
	name, err := validateReadFileURL(args.URL)
	if err != nil {
		return mcpReadFileResult{}, err
	}
	representation := args.Representation
	if representation == "" {
		representation = "auto"
	}
	if representation != "auto" && representation != "image" && representation != "metadata" {
		return mcpReadFileResult{}, &mcpReadError{Code: "unsupported_representation", Message: "representation must be auto, image, or metadata"}
	}
	if args.MaxBytes != nil && *args.MaxBytes < 1 {
		return mcpReadFileResult{}, &mcpReadError{Code: "invalid_file_reference", Message: "max_bytes must be positive"}
	}

	var displayName, ext, pageID, workspaceID string
	var size int64
	err = s.db.QueryRow(`SELECT f.display_name, f.ext, f.size, f.page_id, p.workspace_id
		FROM files f JOIN pages p ON p.id = f.page_id
		WHERE f.file_name = ? AND p.trashed_at IS NULL`, name).
		Scan(&displayName, &ext, &size, &pageID, &workspaceID)
	if err != nil || pageID == "" || !s.canRead(u.ID, pageID) || !s.credentialMayEnter(u, workspaceID) {
		return mcpReadFileResult{}, &mcpReadError{Code: "file_not_found", Message: "file not found"}
	}
	if displayName == "" {
		displayName = name
	}
	path := filepath.Join(s.dataDir, "files", name)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return mcpReadFileResult{}, &mcpReadError{Code: "file_not_found", Message: "file not found"}
		}
		return mcpReadFileResult{}, &mcpReadError{Code: "read_failed", Message: "file could not be read"}
	}
	link, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return mcpReadFileResult{}, &mcpReadError{Code: "file_not_found", Message: "file not found"}
		}
		return mcpReadFileResult{}, &mcpReadError{Code: "read_failed", Message: "file could not be read"}
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, link) {
		return mcpReadFileResult{}, &mcpReadError{Code: "read_failed", Message: "file could not be read"}
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return mcpReadFileResult{}, &mcpReadError{Code: "file_not_found", Message: "file not found"}
		}
		return mcpReadFileResult{}, &mcpReadError{Code: "read_failed", Message: "file could not be read"}
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return mcpReadFileResult{}, &mcpReadError{Code: "read_failed", Message: "file could not be read"}
	}
	size = opened.Size()
	metadata := map[string]any{
		"url": args.URL, "name": displayName, "stored_name": name,
		"mime_type": "application/octet-stream", "size": size,
		"page_id": pageID,
	}
	sample, err := readFilePrefix(f, readFileSampleBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return mcpReadFileResult{}, &mcpReadError{Code: "file_not_found", Message: "file not found"}
		}
		return mcpReadFileResult{}, &mcpReadError{Code: "read_failed", Message: "file could not be read"}
	}
	mimeType := detectReadFileMIME(sample)
	metadata["mime_type"] = mimeType
	if isRasterFileExtension(ext) && !isNativeImageMIME(mimeType) {
		return mcpReadFileResult{}, &mcpReadError{Code: "invalid_image_content", Message: "file is not valid PNG, JPEG, GIF, or WebP content"}
	}
	if representation == "metadata" {
		return mcpReadFileResult{Metadata: metadata, MIME: mimeType}, nil
	}
	if !isNativeImageMIME(mimeType) {
		if representation == "image" {
			return mcpReadFileResult{}, &mcpReadError{Code: "unsupported_representation", Message: "file has no native MCP image representation"}
		}
		return mcpReadFileResult{Metadata: metadata, MIME: mimeType}, nil
	}
	limit := s.maxImageReadBytes()
	if args.MaxBytes != nil && *args.MaxBytes < limit {
		limit = *args.MaxBytes
	}
	if size > limit {
		return mcpReadFileResult{}, &mcpReadError{Code: "image_too_large", Message: "image exceeds the configured read limit"}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return mcpReadFileResult{}, &mcpReadError{Code: "read_failed", Message: "file could not be read"}
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return mcpReadFileResult{}, &mcpReadError{Code: "file_not_found", Message: "file not found"}
		}
		return mcpReadFileResult{}, &mcpReadError{Code: "read_failed", Message: "file could not be read"}
	}
	if int64(len(data)) > limit {
		return mcpReadFileResult{}, &mcpReadError{Code: "image_too_large", Message: "image exceeds the configured read limit"}
	}
	if int64(len(data)) != size || !validNativeImage(data, mimeType) {
		return mcpReadFileResult{}, &mcpReadError{Code: "invalid_image_content", Message: "file is not valid PNG, JPEG, GIF, or WebP content"}
	}
	return mcpReadFileResult{Metadata: metadata, MIME: mimeType, Data: data}, nil
}

func readFilePrefix(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return nil, fmt.Errorf("read file prefix: %w", err)
	}
	return b, nil
}

func validNativeImage(data []byte, mimeType string) bool {
	if detectReadFileMIME(data) != mimeType {
		return false
	}
	if mimeType == "image/webp" {
		return validateWebP(data)
	}
	_, _, err := image.Decode(bytes.NewReader(data))
	return err == nil
}

func isRasterFileExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func isNativeImageMIME(mimeType string) bool {
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}
