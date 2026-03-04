package web

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxMultipartMemory = 64 << 20 // 64 MiB
const maxAttachmentsPerRequest = 10
const maxAttachmentBytes = 25 << 20 // 25 MiB per file

var allowedAttachmentExt = map[string]struct{}{
	".pdf":  {},
	".png":  {},
	".jpg":  {},
	".jpeg": {},
	".webp": {},
	".gif":  {},
	".txt":  {},
	".md":   {},
	".log":  {},
	".json": {},
	".csv":  {},
	".yml":  {},
	".yaml": {},
}

type attachmentValidationError struct {
	msg string
}

func (e attachmentValidationError) Error() string { return e.msg }

func isAttachmentValidationError(err error) bool {
	var v attachmentValidationError
	return errors.As(err, &v)
}

func sanitizeFilename(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "." || base == "" {
		return "file"
	}
	base = strings.ReplaceAll(base, " ", "_")
	base = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, base)
	return base
}

func contentTypeFor(header *multipart.FileHeader) string {
	if header == nil {
		return ""
	}
	if ct := strings.TrimSpace(header.Header.Get("Content-Type")); ct != "" {
		return ct
	}
	return mime.TypeByExtension(strings.ToLower(filepath.Ext(header.Filename)))
}

func allowedAttachment(header *multipart.FileHeader) bool {
	if header == nil {
		return false
	}
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if _, ok := allowedAttachmentExt[ext]; ok {
		return true
	}
	ct := strings.ToLower(contentTypeFor(header))
	if strings.HasPrefix(ct, "image/") {
		return true
	}
	switch ct {
	case "application/pdf", "text/plain", "text/markdown",
		"application/json", "text/csv", "application/x-yaml":
		return true
	default:
		return false
	}
}

func validateAttachments(headers []*multipart.FileHeader) error {
	nonEmpty := 0
	for _, h := range headers {
		if h == nil || h.Size == 0 {
			continue
		}
		nonEmpty++
		if nonEmpty > maxAttachmentsPerRequest {
			return attachmentValidationError{msg: fmt.Sprintf("too many attachments (max %d)", maxAttachmentsPerRequest)}
		}
		if h.Size > maxAttachmentBytes {
			return attachmentValidationError{msg: fmt.Sprintf("attachment %q exceeds %d MB", h.Filename, maxAttachmentBytes/(1<<20))}
		}
		if !allowedAttachment(h) {
			return attachmentValidationError{msg: fmt.Sprintf("attachment type not allowed: %s", h.Filename)}
		}
	}
	return nil
}

func (s *Server) saveAttachments(field string, projectID, taskID int64, headers []*multipart.FileHeader) error {
	_ = field // currently a single upload field is used
	if len(headers) == 0 {
		return nil
	}
	if err := validateAttachments(headers); err != nil {
		return err
	}
	baseDir := filepath.Join(s.cfg.DataDir, "uploads")
	if projectID > 0 {
		baseDir = filepath.Join(baseDir, fmt.Sprintf("project_%d", projectID))
	}
	if taskID > 0 {
		baseDir = filepath.Join(baseDir, fmt.Sprintf("task_%d", taskID))
	}
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return fmt.Errorf("create upload dir: %w", err)
	}
	for _, h := range headers {
		if h == nil || h.Size == 0 {
			continue
		}
		src, err := h.Open()
		if err != nil {
			return fmt.Errorf("open upload %q: %w", h.Filename, err)
		}

		name := sanitizeFilename(h.Filename)
		target := filepath.Join(baseDir, fmt.Sprintf("%d_%s", time.Now().UnixNano(), name))
		dst, err := os.Create(target)
		if err != nil {
			src.Close()
			return fmt.Errorf("create upload file: %w", err)
		}
		n, err := io.Copy(dst, src)
		closeErr := dst.Close()
		src.Close()
		if err != nil {
			return fmt.Errorf("save upload %q: %w", h.Filename, err)
		}
		if closeErr != nil {
			return fmt.Errorf("flush upload %q: %w", h.Filename, closeErr)
		}
		abs, err := filepath.Abs(target)
		if err != nil {
			return fmt.Errorf("resolve upload path: %w", err)
		}
		mimeType := contentTypeFor(h)
		if taskID > 0 {
			if _, err := s.db.CreateTaskAttachment(taskID, h.Filename, abs, mimeType, n); err != nil {
				return fmt.Errorf("record task attachment: %w", err)
			}
		} else {
			if _, err := s.db.CreateProjectAttachment(projectID, h.Filename, abs, mimeType, n); err != nil {
				return fmt.Errorf("record project attachment: %w", err)
			}
		}
	}
	return nil
}
