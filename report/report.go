package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/leighmcculloch/orc/config"
	"github.com/leighmcculloch/orc/state"
)

type Entry struct {
	TaskID     string    `json:"task_id"`
	Prompt     string    `json:"prompt"`
	Status     string    `json:"status"`
	FinishedAt time.Time `json:"finished_at"`
}

type DailyReport struct {
	Date    string  `json:"date"`
	Entries []Entry `json:"entries"`
}

func RecordCompletion(task state.Task) error {
	if err := config.EnsureOrcDir(); err != nil {
		return err
	}

	date := time.Now().Format("2006-01-02")
	reportPath := filepath.Join(config.OrcDir(), "reports", fmt.Sprintf("%s.json", date))

	var daily DailyReport
	if data, err := os.ReadFile(reportPath); err == nil {
		json.Unmarshal(data, &daily)
	}
	daily.Date = date

	entry := Entry{
		TaskID: task.ID,
		Prompt: task.Prompt,
		Status: string(task.Status),
	}
	if task.FinishedAt != nil {
		entry.FinishedAt = *task.FinishedAt
	}
	daily.Entries = append(daily.Entries, entry)

	data, err := json.MarshalIndent(daily, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(reportPath, data, 0644)
}

func GetReport(date string) (DailyReport, error) {
	reportPath := filepath.Join(config.OrcDir(), "reports", fmt.Sprintf("%s.json", date))
	data, err := os.ReadFile(reportPath)
	if err != nil {
		if os.IsNotExist(err) {
			return DailyReport{Date: date, Entries: []Entry{}}, nil
		}
		return DailyReport{}, err
	}
	var daily DailyReport
	if err := json.Unmarshal(data, &daily); err != nil {
		return DailyReport{}, err
	}
	return daily, nil
}
