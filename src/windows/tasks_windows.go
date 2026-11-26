//go:build windows

package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

type taskLogLine struct {
	Ts    string `json:"ts"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

type taskRec struct {
	mu       sync.Mutex
	ID       string
	Device   string
	Fs       string
	Label    string
	Mode     string
	Status   string
	Stage    string
	Progress float64
	Logs     []taskLogLine
	Kind     string
}

var (
	taskMu sync.Mutex
	tasks  = map[string]*taskRec{}
	taskN  int
)

func newTask(kind, device, fs, label, mode string) *taskRec {
	taskMu.Lock()
	defer taskMu.Unlock()
	taskN++
	id := fmt.Sprintf("T%03d", taskN)
	// 任务只用于界面轮询，保留最近 60 个就够；以前只增不减，
	// 长时间挂着会一直攒内存和日志。
	if len(tasks) > 60 {
		oldest, oldestN := "", 1<<30
		for k, t := range tasks {
			if t == nil {
				continue
			}
			n, err := strconv.Atoi(strings.TrimPrefix(t.ID, "T"))
			if err != nil {
				continue
			}
			if n < oldestN {
				oldestN, oldest = n, k
			}
		}
		if oldest != "" {
			delete(tasks, oldest)
		}
	}
	t := &taskRec{ID: id, Device: device, Fs: fs, Label: label, Mode: mode, Status: "running", Stage: "准备中", Progress: 0.02, Kind: kind}
	tasks[id] = t
	return t
}

func getTask(id string) *taskRec {
	taskMu.Lock()
	defer taskMu.Unlock()
	return tasks[id]
}

func (t *taskRec) log(level, msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Logs = append(t.Logs, taskLogLine{Ts: time.Now().Format("15:04:05"), Level: level, Msg: msg})
	if len(t.Logs) > 400 {
		t.Logs = t.Logs[len(t.Logs)-400:]
	}
}

func (t *taskRec) stage(stage string, p float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if stage != "" {
		t.Stage = stage
	}
	if p >= 0 && p <= 1 {
		t.Progress = p
	}
}

func (t *taskRec) addProgress(p float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Progress += p
	if t.Progress > 0.99 {
		t.Progress = 0.99
	}
}

func (t *taskRec) finish(ok bool, msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ok {
		t.Status = "done"
		t.Progress = 1
		t.Stage = "完成"
		if msg != "" {
			t.Logs = append(t.Logs, taskLogLine{Ts: time.Now().Format("15:04:05"), Level: "ok", Msg: msg})
		}
	} else {
		t.Status = "failed"
		t.Stage = "失败"
		if msg != "" {
			t.Logs = append(t.Logs, taskLogLine{Ts: time.Now().Format("15:04:05"), Level: "error", Msg: msg})
		}
	}
}

func (t *taskRec) snapshot() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	logs := make([]taskLogLine, len(t.Logs))
	copy(logs, t.Logs)
	return map[string]any{
		"id": t.ID, "device": t.Device, "fs": t.Fs, "label": t.Label, "mode": t.Mode,
		"status": t.Status, "stage": t.Stage, "progress": t.Progress, "log": logs, "kind": t.Kind,
	}
}
