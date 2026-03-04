package daemon

import (
	"fmt"
	"strings"

	"github.com/florian/kodama/internal/db"
)

func formatAttachmentList(projectFiles, taskFiles []*db.Attachment) string {
	var b strings.Builder
	if len(projectFiles) > 0 {
		b.WriteString("Project attachments:\n")
		for _, a := range projectFiles {
			b.WriteString(fmt.Sprintf("- %s (%s) at %s\n", a.Name, a.MimeType, a.Path))
		}
	}
	if len(taskFiles) > 0 {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("Task attachments:\n")
		for _, a := range taskFiles {
			b.WriteString(fmt.Sprintf("- %s (%s) at %s\n", a.Name, a.MimeType, a.Path))
		}
	}
	return strings.TrimSpace(b.String())
}

func injectAttachmentContext(prompt string, projectFiles, taskFiles []*db.Attachment) string {
	list := formatAttachmentList(projectFiles, taskFiles)
	if list == "" {
		return prompt
	}
	return fmt.Sprintf("Reference files are available for this task:\n%s\n\nUse them as needed.\n\n%s", list, prompt)
}
