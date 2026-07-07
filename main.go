package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxBodySize = 1024 * 1024

type record struct {
	ID        string          `json:"id"`
	CreatedAt string          `json:"createdAt"`
	Data      json.RawMessage `json:"data"`
}

type searchResponse struct {
	Query   string   `json:"query"`
	Count   int      `json:"count"`
	Records []record `json:"records"`
}

type app struct {
	dataFile string
	mu       sync.RWMutex
	records  map[string]record
}

func main() {
	port := getenv("PORT", "8080")
	dataFile := getenv("DATA_FILE", filepath.Join("data", "assessments.json"))

	application := &app{
		dataFile: dataFile,
		records:  map[string]record{},
	}

	if err := application.load(); err != nil {
		log.Fatalf("load records: %v", err)
	}

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           application.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("Nutritional assessment API listening on http://localhost:%s", port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func getenv(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", a.handleHealth)
	mux.HandleFunc("/api/assessments", a.handleAssessments)
	mux.HandleFunc("/api/assessments/", a.handleAssessmentByID)
	mux.HandleFunc("/api/send", a.handleAssessments)
	mux.HandleFunc("/api/send/", a.handleAssessmentByID)
	return withCORS(mux)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Cache-Control", "no-store")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (a *app) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed."})
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *app) handleAssessments(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		a.createAssessment(w, r)
	case http.MethodGet:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if id != "" {
			a.getAssessment(w, id)
			return
		}
		if name != "" {
			a.searchAssessmentsByName(w, name)
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Assessment id or name is required."})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed."})
	}
}

func (a *app) handleAssessmentByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed."})
		return
	}

	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/assessments/"), "/")
	if strings.HasPrefix(r.URL.Path, "/api/send/") {
		id = strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/send/"), "/")
	}
	if id == "" || strings.Contains(id, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Assessment id is required."})
		return
	}

	a.getAssessment(w, id)
}

func (a *app) createAssessment(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Unable to read request body."})
		return
	}
	defer r.Body.Close()

	if len(raw) > maxBodySize {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "Request body is too large."})
		return
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Request body is required."})
		return
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Request body must be a JSON object."})
		return
	}

	a.mu.Lock()
	now := time.Now()
	id := timestampID(now)
	for {
		if _, exists := a.records[id]; !exists {
			break
		}
		now = now.Add(time.Second)
		id = timestampID(now)
	}

	createdRecord := record{
		ID:        id,
		CreatedAt: now.Format(time.RFC3339Nano),
		Data:      json.RawMessage(raw),
	}

	a.records[id] = createdRecord
	if err := a.saveLocked(); err != nil {
		delete(a.records, id)
		a.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Unable to save assessment."})
		return
	}
	a.mu.Unlock()

	writeJSON(w, http.StatusCreated, createdRecord)
}

func (a *app) getAssessment(w http.ResponseWriter, id string) {
	a.mu.RLock()
	savedRecord, ok := a.records[id]
	a.mu.RUnlock()

	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Assessment not found."})
		return
	}

	writeJSON(w, http.StatusOK, savedRecord)
}

func (a *app) searchAssessmentsByName(w http.ResponseWriter, name string) {
	normalizedQuery := strings.ToLower(strings.TrimSpace(name))
	if normalizedQuery == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Assessment name is required."})
		return
	}

	matches := []record{}

	a.mu.RLock()
	for _, savedRecord := range a.records {
		respondentName := strings.ToLower(recordRespondentName(savedRecord))
		if respondentName != "" && strings.Contains(respondentName, normalizedQuery) {
			matches = append(matches, savedRecord)
		}
	}
	a.mu.RUnlock()

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].ID > matches[j].ID
	})

	writeJSON(w, http.StatusOK, searchResponse{
		Query:   name,
		Count:   len(matches),
		Records: matches,
	})
}

func recordRespondentName(savedRecord record) string {
	var payload struct {
		Respondent struct {
			Name string `json:"name"`
		} `json:"respondent"`
	}
	if err := json.Unmarshal(savedRecord.Data, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Respondent.Name)
}

func (a *app) load() error {
	raw, err := os.ReadFile(a.dataFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}

	return json.Unmarshal(raw, &a.records)
}

func (a *app) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(a.dataFile), 0755); err != nil {
		return err
	}

	raw, err := json.MarshalIndent(a.records, "", "  ")
	if err != nil {
		return err
	}

	tempFile := a.dataFile + ".tmp"
	if err := os.WriteFile(tempFile, raw, 0644); err != nil {
		return err
	}
	return os.Rename(tempFile, a.dataFile)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("write json response: %v", err)
	}
}

func timestampID(t time.Time) string {
	return t.Format("20060102150405")
}
