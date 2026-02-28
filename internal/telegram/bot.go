package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Bot is the Telegram bot for Kodama notifications and question answering.
type Bot struct {
	api    *tgbotapi.BotAPI
	userID int64

	// Pending question channels keyed by task ID.
	questions map[int64]chan string
	mu        sync.Mutex

	service TaskService
}

// TaskService provides project/task operations for Telegram commands.
type TaskService interface {
	ListProjects() ([]ProjectInfo, error)
	ListTasks(projectID int64) ([]TaskInfo, error)
	CreateTask(projectID int64, description string) error
	StartProject(projectID int64) error
}

// ProjectInfo is a minimal project representation for Telegram.
type ProjectInfo struct {
	ID   int64
	Name string
}

// TaskInfo is a minimal task representation for Telegram.
type TaskInfo struct {
	ID          int64
	Status      string
	Description string
}

// New creates and returns a new Bot.
// token is the Telegram bot token; userID is the single whitelisted user.
func New(token string, userID int64, service TaskService) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}
	return &Bot{
		api:       api,
		userID:    userID,
		questions: make(map[int64]chan string),
		service:   service,
	}, nil
}

// Start begins polling for incoming Telegram messages.
func (b *Bot) Start(ctx context.Context) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30

	updates := b.api.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			b.api.StopReceivingUpdates()
			return
		case update, ok := <-updates:
			if !ok {
				return
			}
			if update.Message == nil {
				continue
			}
			// Whitelist enforcement — silently ignore all other users.
			if update.Message.From.ID != b.userID {
				slog.Debug("ignoring message from non-whitelisted user", "user_id", update.Message.From.ID)
				continue
			}
			b.handleMessage(update.Message)
		}
	}
}

// SendNotification sends a text notification to the whitelisted user.
func (b *Bot) SendNotification(msg string) {
	if b.userID == 0 {
		return
	}
	m := tgbotapi.NewMessage(b.userID, msg)
	if _, err := b.api.Send(m); err != nil {
		slog.Error("telegram send notification", "err", err)
	}
}

// SendQuestion sends a question notification and returns a channel for the reply.
// The channel will receive exactly one string (the user's reply).
func (b *Bot) SendQuestion(taskID int64, question string) (<-chan string, error) {
	ch := make(chan string, 1)

	b.mu.Lock()
	b.questions[taskID] = ch
	b.mu.Unlock()

	msg := fmt.Sprintf("Task #%d is waiting for input:\n\n%s\n\nReply with: /answer %d <your answer>",
		taskID, question, taskID)
	b.SendNotification(msg)

	return ch, nil
}

// handleMessage processes an incoming Telegram message from the whitelisted user.
func (b *Bot) handleMessage(msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)

	// Handle /answer <taskID> <answer> command.
	if strings.HasPrefix(text, "/answer ") {
		parts := strings.SplitN(text[len("/answer "):], " ", 2)
		if len(parts) < 2 {
			b.reply(msg, "Usage: /answer <task_id> <your answer>")
			return
		}
		taskID, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil {
			b.reply(msg, "Invalid task ID")
			return
		}
		answer := strings.TrimSpace(parts[1])

		b.mu.Lock()
		ch, ok := b.questions[taskID]
		if ok {
			delete(b.questions, taskID)
		}
		b.mu.Unlock()

		if !ok {
			b.reply(msg, fmt.Sprintf("No waiting question for task #%d", taskID))
			return
		}

		ch <- answer
		b.reply(msg, fmt.Sprintf("Answer sent to task #%d", taskID))
		return
	}

	// /projects -> list projects
	if text == "/projects" {
		if b.service == nil {
			b.reply(msg, "Project listing is not configured.")
			return
		}
		projects, err := b.service.ListProjects()
		if err != nil {
			b.reply(msg, "Failed to list projects.")
			return
		}
		if len(projects) == 0 {
			b.reply(msg, "No projects found.")
			return
		}
		var sb strings.Builder
		sb.WriteString("Projects:\n")
		for _, p := range projects {
			sb.WriteString(fmt.Sprintf("- %d: %s\n", p.ID, p.Name))
		}
		b.reply(msg, sb.String())
		return
	}

	// /tasks <project_id> -> list tasks
	if strings.HasPrefix(text, "/tasks ") {
		if b.service == nil {
			b.reply(msg, "Task listing is not configured.")
			return
		}
		idStr := strings.TrimSpace(text[len("/tasks "):])
		projectID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			b.reply(msg, "Usage: /tasks <project_id>")
			return
		}
		tasks, err := b.service.ListTasks(projectID)
		if err != nil {
			b.reply(msg, "Failed to list tasks.")
			return
		}
		if len(tasks) == 0 {
			b.reply(msg, "No tasks found.")
			return
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Tasks for project %d:\n", projectID))
		for _, t := range tasks {
			sb.WriteString(fmt.Sprintf("- #%d [%s] %s\n", t.ID, t.Status, t.Description))
		}
		b.reply(msg, sb.String())
		return
	}

	// /task <project_id> <description> -> create task
	if strings.HasPrefix(text, "/task ") {
		if b.service == nil {
			b.reply(msg, "Task creation is not configured.")
			return
		}
		parts := strings.SplitN(text[len("/task "):], " ", 2)
		if len(parts) < 2 {
			b.reply(msg, "Usage: /task <project_id> <description>")
			return
		}
		projectID, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil {
			b.reply(msg, "Invalid project ID")
			return
		}
		desc := strings.TrimSpace(parts[1])
		if desc == "" {
			b.reply(msg, "Description cannot be empty")
			return
		}
		if err := b.service.CreateTask(projectID, desc); err != nil {
			b.reply(msg, "Failed to create task.")
			return
		}
		b.reply(msg, fmt.Sprintf("Task added to project %d", projectID))
		return
	}

	// /work <project_id> -> start backlog processing
	if text == "/work" {
		b.reply(msg, "Usage: /work <project_id>")
		return
	}
	if strings.HasPrefix(text, "/work ") {
		if b.service == nil {
			b.reply(msg, "Start is not configured.")
			return
		}
		idStr := strings.TrimSpace(text[len("/work "):])
		projectID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			b.reply(msg, "Usage: /work <project_id>")
			return
		}
		if err := b.service.StartProject(projectID); err != nil {
			b.reply(msg, "Failed to start project.")
			return
		}
		b.reply(msg, fmt.Sprintf("Started project %d", projectID))
		return
	}

	if text == "/help" {
		b.reply(msg, "Commands:\n/projects\n/tasks <project_id>\n/task <project_id> <description>\n/work <project_id>\n/answer <task_id> <answer>\n/help")
		return
	}

	// If there's only one pending question, treat any reply as the answer.
	b.mu.Lock()
	if len(b.questions) == 1 {
		for taskID, ch := range b.questions {
			delete(b.questions, taskID)
			b.mu.Unlock()
			ch <- text
			b.reply(msg, fmt.Sprintf("Answer sent to task #%d", taskID))
			return
		}
	}
	b.mu.Unlock()

	// Default: echo help.
	b.reply(msg, "Commands:\n/projects\n/tasks <project_id>\n/task <project_id> <description>\n/work <project_id>\n/answer <task_id> <answer>\n/help")
}

// reply sends a reply message.
func (b *Bot) reply(orig *tgbotapi.Message, text string) {
	m := tgbotapi.NewMessage(orig.Chat.ID, text)
	m.ReplyToMessageID = orig.MessageID
	if _, err := b.api.Send(m); err != nil {
		slog.Error("telegram reply", "err", err)
	}
}
