package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

const (
	a2aTaskStateSubmitted     = "submitted"
	a2aTaskStateWorking       = "working"
	a2aTaskStateInputRequired = "input_required"
	a2aTaskStateCompleted     = "completed"
	a2aTaskStateFailed        = "failed"
	a2aTaskStateCanceled      = "canceled"
	a2aTaskStateRejected      = "rejected"
	a2aTaskMaxStored          = 1000
	a2aTaskMaxEvents          = 50
)

type a2aTask struct {
	ID          string         `json:"id"`
	State       string         `json:"state"`
	Skill       string         `json:"skill"`
	Arguments   map[string]any `json:"arguments,omitempty"`
	Result      any            `json:"result,omitempty"`
	Error       map[string]any `json:"error,omitempty"`
	StatusCode  int            `json:"status_code,omitempty"`
	InputHash   string         `json:"input_hash"`
	ResultHash  string         `json:"result_hash,omitempty"`
	EpisodeID   string         `json:"episode_id,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
	Events      []a2aTaskEvent `json:"events,omitempty"`
}

type a2aTaskEvent struct {
	ID        string         `json:"id"`
	TaskID    string         `json:"task_id"`
	State     string         `json:"state"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"data,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

func (s *Server) handleA2ATaskCreate(w http.ResponseWriter, r *http.Request) {
	var req a2aRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}
	action := normalizeA2AAction(firstNonEmpty(req.Skill, req.Action, req.Name, req.Method))
	args := firstMap(req.Arguments, req.Params, req.Input)
	if action == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "skill is required"})
		return
	}

	task := &a2aTask{
		ID:         newA2ATaskID(),
		State:      a2aTaskStateSubmitted,
		Skill:      action,
		Arguments:  args,
		StatusCode: http.StatusAccepted,
		InputHash:  hashAny(map[string]any{"skill": action, "arguments": args}),
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	task.Events = append(task.Events, newA2ATaskEvent(task.ID, a2aTaskStateSubmitted, "Task submitted.", nil))
	s.storeA2ATask(task)

	headers := r.Header.Clone()
	host := r.Host
	remoteAddr := r.RemoteAddr
	go s.runA2ATask(task.ID, headers, host, remoteAddr)

	writeJSON(w, http.StatusAccepted, s.a2aTaskPublicView(task, publicBaseURL(r)))
}

func (s *Server) handleA2ATaskGet(w http.ResponseWriter, r *http.Request) {
	task, ok := s.getA2ATask(strings.TrimSpace(r.PathValue("id")))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "task not found"})
		return
	}
	writeJSON(w, http.StatusOK, s.a2aTaskPublicView(task, publicBaseURL(r)))
}

func (s *Server) handleA2ATaskEvents(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if _, ok := s.getA2ATask(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "task not found"})
		return
	}
	streamSSE(w, r, func(context.Context) (sseUpdate, bool) {
		task, ok := s.getA2ATask(id)
		if !ok {
			return sseUpdate{
				Key:     "missing",
				Payload: map[string]any{"id": id, "state": a2aTaskStateFailed, "error": "task not found"},
				Final:   true,
			}, true
		}
		return sseUpdate{
			Key:     task.UpdatedAt.Format(time.RFC3339Nano),
			Payload: s.a2aTaskPublicView(task, publicBaseURL(r)),
			Final:   isFinalA2ATaskState(task.State),
		}, true
	})
}

func (s *Server) runA2ATask(id string, headers http.Header, host, remoteAddr string) {
	task, ok := s.getA2ATask(id)
	if !ok {
		return
	}
	started := time.Now()
	s.updateA2ATask(id, func(task *a2aTask) {
		task.State = a2aTaskStateWorking
		task.StatusCode = http.StatusProcessing
		task.UpdatedAt = time.Now().UTC()
		task.Events = appendA2ATaskEvent(task.Events, newA2ATaskEvent(task.ID, a2aTaskStateWorking, "Task execution started.", nil))
	})

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/a2a/tasks/"+id, nil)
	req.Header = headers.Clone()
	req.Host = host
	req.RemoteAddr = remoteAddr

	result, status, errPayload := s.executeA2AAction(req, task.Skill, task.Arguments)
	now := time.Now().UTC()
	state := a2aTaskStateCompleted
	message := "Task completed."
	if errPayload != nil {
		state = classifyA2ATaskFailure(status)
		message = "Task failed."
	}
	episode := agentEpisode{
		AgentID:          "chainfx-agent-pay",
		Protocol:         "a2a_task",
		Skill:            task.Skill,
		InputHash:        task.InputHash,
		PaymentIntentID:  paymentIntentIDFromAny(result),
		SettlementStatus: settlementStatusFromAny(result),
		LatencyMS:        time.Since(started).Milliseconds(),
		ResultHash:       hashAny(result),
		ErrorTree:        errPayload,
		Status:           state,
		StatusCode:       status,
		CreatedAt:        started.UTC(),
	}
	if episode.Status == a2aTaskStateInputRequired || episode.Status == a2aTaskStateRejected {
		episode.Status = a2aTaskStateFailed
	}
	episode.EpisodeID = newAgentEpisodeID(episode.Skill, episode.InputHash, episode.CreatedAt)
	s.recordAgentEpisode(episode)

	s.updateA2ATask(id, func(task *a2aTask) {
		task.State = state
		task.Result = result
		task.Error = errPayload
		task.StatusCode = status
		task.ResultHash = hashAny(result)
		task.EpisodeID = episode.EpisodeID
		task.UpdatedAt = now
		task.CompletedAt = &now
		task.Events = appendA2ATaskEvent(task.Events, newA2ATaskEvent(task.ID, state, message, map[string]any{
			"status_code": status,
			"episode_id":  episode.EpisodeID,
		}))
	})
}

func (s *Server) storeA2ATask(task *a2aTask) {
	s.a2aTasksMu.Lock()
	defer s.a2aTasksMu.Unlock()
	if s.a2aTasks == nil {
		s.a2aTasks = make(map[string]*a2aTask)
	}
	s.a2aTasks[task.ID] = cloneA2ATask(task)
	if len(s.a2aTasks) <= a2aTaskMaxStored {
		return
	}
	var oldestID string
	var oldest time.Time
	for id, item := range s.a2aTasks {
		if oldestID == "" || item.CreatedAt.Before(oldest) {
			oldestID = id
			oldest = item.CreatedAt
		}
	}
	delete(s.a2aTasks, oldestID)
}

func (s *Server) getA2ATask(id string) (*a2aTask, bool) {
	if id == "" {
		return nil, false
	}
	s.a2aTasksMu.Lock()
	defer s.a2aTasksMu.Unlock()
	task, ok := s.a2aTasks[id]
	if !ok {
		return nil, false
	}
	return cloneA2ATask(task), true
}

func (s *Server) updateA2ATask(id string, mutate func(*a2aTask)) {
	s.a2aTasksMu.Lock()
	defer s.a2aTasksMu.Unlock()
	task, ok := s.a2aTasks[id]
	if !ok {
		return
	}
	mutate(task)
}

func (s *Server) a2aTaskPublicView(task *a2aTask, base string) map[string]any {
	out := map[string]any{
		"id":          task.ID,
		"state":       task.State,
		"skill":       task.Skill,
		"status_code": task.StatusCode,
		"input_hash":  task.InputHash,
		"created_at":  task.CreatedAt,
		"updated_at":  task.UpdatedAt,
		"events_url":  base + "/a2a/tasks/" + task.ID + "/events",
		"status_url":  base + "/a2a/tasks/" + task.ID,
	}
	if task.Result != nil {
		out["result"] = task.Result
	}
	if task.Error != nil {
		out["error"] = task.Error
	}
	if task.ResultHash != "" {
		out["result_hash"] = task.ResultHash
	}
	if task.EpisodeID != "" {
		out["episode_id"] = task.EpisodeID
	}
	if task.CompletedAt != nil {
		out["completed_at"] = task.CompletedAt
	}
	if len(task.Events) > 0 {
		out["events"] = task.Events
	}
	return out
}

func appendA2ATaskEvent(events []a2aTaskEvent, event a2aTaskEvent) []a2aTaskEvent {
	events = append(events, event)
	if len(events) > a2aTaskMaxEvents {
		return append([]a2aTaskEvent(nil), events[len(events)-a2aTaskMaxEvents:]...)
	}
	return events
}

func newA2ATaskEvent(taskID, state, message string, data map[string]any) a2aTaskEvent {
	now := time.Now().UTC()
	return a2aTaskEvent{
		ID:        newA2AEventID(taskID, state, now),
		TaskID:    taskID,
		State:     state,
		Message:   message,
		Data:      data,
		CreatedAt: now,
	}
}

func newA2ATaskID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "task_" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	}
	return "task_" + hex.EncodeToString(raw[:])
}

func newA2AEventID(taskID, state string, now time.Time) string {
	raw := hashAny(map[string]any{"task_id": taskID, "state": state, "created_at": now.Format(time.RFC3339Nano)})
	if len(raw) > 24 {
		raw = raw[:24]
	}
	return "evt_" + raw
}

func cloneA2ATask(task *a2aTask) *a2aTask {
	if task == nil {
		return nil
	}
	clone := *task
	clone.Events = append([]a2aTaskEvent(nil), task.Events...)
	return &clone
}

func classifyA2ATaskFailure(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return a2aTaskStateInputRequired
	case http.StatusUnauthorized, http.StatusForbidden:
		return a2aTaskStateRejected
	default:
		return a2aTaskStateFailed
	}
}

func isFinalA2ATaskState(state string) bool {
	switch state {
	case a2aTaskStateCompleted, a2aTaskStateFailed, a2aTaskStateCanceled, a2aTaskStateRejected, a2aTaskStateInputRequired:
		return true
	default:
		return false
	}
}

func supportedA2ASkills() []string {
	return []string{
		"pay_pix_with_usdt",
		"pay_card_bill_with_usdt",
		"get_payment_status",
		"quote_required_usdt",
		"list_supported_payment_methods",
		"stablecoin_exchange",
		"capability_exchange",
		"semantic_memory",
		"document_ocr",
		"llm_chat",
	}
}
