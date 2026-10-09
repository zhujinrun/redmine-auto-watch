package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type State struct {
	LastUpdated string `json:"last_updated"`
	// LastReportDate 每日报告最近发送日期 "2006-01-02", 用于同天去重
	LastReportDate string           `json:"last_report_date,omitempty"`
	SeenJournals   map[string]int64 `json:"seen_journals"`
}

func loadState(baseDir string) *State {
	st := &State{SeenJournals: make(map[string]int64)}
	path := filepath.Join(baseDir, "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, st)
	if st.SeenJournals == nil {
		st.SeenJournals = make(map[string]int64)
	}
	return st
}

func saveState(baseDir string, st *State) error {
	path := filepath.Join(baseDir, "state.json")
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
