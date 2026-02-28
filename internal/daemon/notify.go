package daemon

import "fmt"

func formatTaskMsg(projectName string, taskID int64, msg string) string {
	if projectName != "" {
		if taskID == 0 {
			return fmt.Sprintf("[%s] %s", projectName, msg)
		}
		return fmt.Sprintf("[%s] Task #%d %s", projectName, taskID, msg)
	}
	return fmt.Sprintf("Task #%d %s", taskID, msg)
}
