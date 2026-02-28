package daemon

import "fmt"

func formatTaskMsg(projectName string, taskID int64, msg string) string {
	if projectName != "" {
		return fmt.Sprintf("[%s] Task #%d %s", projectName, taskID, msg)
	}
	return fmt.Sprintf("Task #%d %s", taskID, msg)
}
