package daemon

import (
	"testing"

	"github.com/florian/kodama/internal/config"
	"github.com/florian/kodama/internal/db"
	"github.com/stretchr/testify/require"
)

func TestExtractPlannedTasks(t *testing.T) {
	out := `hello
KODAMA_TASKS_BEGIN
[
  {"title":"A","description":"Do A","priority":1,"profile":"developer","agent":"codex","failover":false},
  {"title":"B","description":"Do B","priority":2,"profile":"qa","agent":"","failover":false}
]
KODAMA_TASKS_END
bye`
	tasks, err := extractPlannedTasks(out)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	require.Equal(t, "A", tasks[0].Title)
	require.Equal(t, "qa", tasks[1].Profile)
}

func TestExtractPlannedTasksUsesLastValidBlock(t *testing.T) {
	out := `echoing instructions
KODAMA_TASKS_BEGIN
[
  {"title":"short task title","description":"template","priority":0,"profile":"developer","agent":"","failover":false}
]
KODAMA_TASKS_END

final answer
KODAMA_TASKS_BEGIN
[
  {"title":"Implement auth","description":"JWT login flow","priority":1,"profile":"developer","agent":"codex","failover":false},
  {"title":"Add QA checks","description":"Regression checklist","priority":2,"profile":"qa","agent":"","failover":false}
]
KODAMA_TASKS_END`

	tasks, err := extractPlannedTasks(out)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	require.Equal(t, "Implement auth", tasks[0].Title)
	require.Equal(t, "qa", tasks[1].Profile)
}

func TestImportPlannedTasks(t *testing.T) {
	database, err := db.Open(t.TempDir())
	require.NoError(t, err)
	defer database.Close()
	d := New(&config.Config{}, database, nil, nil)

	proj, err := database.CreateProject("p", "/tmp", "", "codex", false)
	require.NoError(t, err)
	_, err = database.CreateTask(proj.ID, "Existing", "", 0, false)
	require.NoError(t, err)

	planned := []plannedTask{
		{Title: "Existing", Description: "", Priority: 0, Profile: "developer", Agent: "", Failover: false},
		{Title: "New task", Description: "Do work", Priority: 1, Profile: "qa", Agent: "claude", Failover: true},
	}
	n, err := d.importPlannedTasks(proj.ID, planned)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	tasks, err := database.ListTasks(proj.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	require.Equal(t, "qa", tasks[1].Profile)
	require.Equal(t, "claude", tasks[1].Agent)
	require.Equal(t, true, tasks[1].Failover)
}
