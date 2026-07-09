package main

import (
	"context"
	"database/sql"
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

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
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

type recordStore interface {
	Create(raw json.RawMessage, now time.Time) (record, error)
	Get(id string) (record, bool, error)
	SearchByName(name string) ([]record, error)
	Close() error
}

type app struct {
	store recordStore
}

type fileStore struct {
	dataFile string
	mu       sync.RWMutex
	records  map[string]record
}

type postgresStore struct {
	db *sql.DB
}

func main() {
	port := getenv("PORT", "8080")
	dataFile := getenv("DATA_FILE", filepath.Join("data", "assessments.json"))

	store, err := newStore(dataFile, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("init storage: %v", err)
	}
	defer store.Close()

	application := &app{store: store}

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

func newStore(dataFile string, databaseURL string) (recordStore, error) {
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL != "" {
		store, err := newPostgresStore(databaseURL)
		if err != nil {
			return nil, err
		}
		log.Println("Using PostgreSQL assessment storage.")
		return store, nil
	}

	store, err := newFileStore(dataFile)
	if err != nil {
		return nil, err
	}
	log.Printf("Using JSON file assessment storage: %s", dataFile)
	return store, nil
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", a.handleHealth)
	mux.HandleFunc("/ping", a.handlePing)
	mux.HandleFunc("/api/assessments", a.handleAssessments)
	mux.HandleFunc("/api/assessments/", a.handleAssessmentByID)
	mux.HandleFunc("/api/send", a.handleAssessments)
	mux.HandleFunc("/api/send/", a.handleAssessmentByID)
	return withRequestLogging(withCORS(mux))
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(body)
}

func withRequestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}

		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.RequestURI(), status, time.Since(started).Round(time.Millisecond))
	})
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := w.Header()
		requestHeaders := strings.TrimSpace(r.Header.Get("Access-Control-Request-Headers"))
		if requestHeaders == "" {
			requestHeaders = "Accept, Authorization, Content-Type, X-Requested-With"
		}

		headers.Set("Access-Control-Allow-Origin", "*")
		headers.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		headers.Set("Access-Control-Allow-Headers", requestHeaders)
		headers.Set("Access-Control-Max-Age", "86400")
		headers.Add("Vary", "Origin")
		headers.Add("Vary", "Access-Control-Request-Method")
		headers.Add("Vary", "Access-Control-Request-Headers")
		headers.Set("Cache-Control", "no-store")

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

func (a *app) handlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed."})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "pong"})
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
	log.Printf(
		"POST %s name=%s date=%s answers=%d",
		r.URL.RequestURI(),
		logValue(payloadRespondentName(payload)),
		logValue(payloadRespondentDate(payload)),
		payloadCheckedAnswerCount(payload),
	)

	createdRecord, err := a.store.Create(json.RawMessage(raw), time.Now())
	if err != nil {
		log.Printf("save assessment: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Unable to save assessment."})
		return
	}

	writeJSON(w, http.StatusCreated, createdRecord)
}

func (a *app) getAssessment(w http.ResponseWriter, id string) {
	savedRecord, ok, err := a.store.Get(id)
	if err != nil {
		log.Printf("get assessment %q: %v", id, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Unable to get assessment."})
		return
	}

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

	matches, err := a.store.SearchByName(normalizedQuery)
	if err != nil {
		log.Printf("search assessments by name %q: %v", name, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Unable to search assessments."})
		return
	}

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

func payloadRespondentName(payload map[string]any) string {
	respondent, ok := payload["respondent"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := respondent["name"].(string)
	return strings.TrimSpace(name)
}

func payloadRespondentDate(payload map[string]any) string {
	respondent, ok := payload["respondent"].(map[string]any)
	if !ok {
		return ""
	}
	date, _ := respondent["date"].(string)
	return strings.TrimSpace(date)
}

func payloadCheckedAnswerCount(payload map[string]any) int {
	rawAnswers, ok := payload["answers"].([]any)
	if !ok {
		return 0
	}

	count := 0
	for _, rawAnswer := range rawAnswers {
		answer, ok := rawAnswer.(map[string]any)
		if !ok {
			continue
		}
		checked, _ := answer["checked"].(bool)
		if checked {
			count++
		}
	}
	return count
}

func logValue(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func newFileStore(dataFile string) (*fileStore, error) {
	store := &fileStore{
		dataFile: dataFile,
		records:  map[string]record{},
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *fileStore) Create(raw json.RawMessage, now time.Time) (record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := timestampID(now)
	for {
		if _, exists := s.records[id]; !exists {
			break
		}
		now = now.Add(time.Second)
		id = timestampID(now)
	}

	createdRecord := record{
		ID:        id,
		CreatedAt: now.Format(time.RFC3339Nano),
		Data:      raw,
	}

	s.records[id] = createdRecord
	if err := s.saveLocked(); err != nil {
		delete(s.records, id)
		return record{}, err
	}

	return createdRecord, nil
}

func (s *fileStore) Get(id string) (record, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	savedRecord, ok := s.records[id]
	return savedRecord, ok, nil
}

func (s *fileStore) SearchByName(name string) ([]record, error) {
	matches := []record{}

	s.mu.RLock()
	for _, savedRecord := range s.records {
		respondentName := strings.ToLower(recordRespondentName(savedRecord))
		if respondentName != "" && strings.Contains(respondentName, name) {
			matches = append(matches, savedRecord)
		}
	}
	s.mu.RUnlock()

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].ID > matches[j].ID
	})

	return matches, nil
}

func (s *fileStore) Close() error {
	return nil
}

func (s *fileStore) load() error {
	raw, err := os.ReadFile(s.dataFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}

	return json.Unmarshal(raw, &s.records)
}

func (s *fileStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.dataFile), 0755); err != nil {
		return err
	}

	raw, err := json.MarshalIndent(s.records, "", "  ")
	if err != nil {
		return err
	}

	tempFile := s.dataFile + ".tmp"
	if err := os.WriteFile(tempFile, raw, 0644); err != nil {
		return err
	}
	return os.Rename(tempFile, s.dataFile)
}

func newPostgresStore(databaseURL string) (*postgresStore, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	store := &postgresStore{db: db}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.ensureSchema(ctx); err != nil {
		db.Close()
		return nil, err
	}

	return store, nil
}

func (s *postgresStore) ensureSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS assessments (
			id TEXT PRIMARY KEY,
			created_at TIMESTAMPTZ NOT NULL,
			data JSONB NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS assessments_name_idx
			ON assessments ((lower(data #>> '{respondent,name}')))`,
	}

	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *postgresStore) Create(raw json.RawMessage, now time.Time) (record, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for attempts := 0; attempts < 10; attempts++ {
		createdRecord := record{
			ID:        timestampID(now),
			CreatedAt: now.Format(time.RFC3339Nano),
			Data:      raw,
		}

		_, err := s.db.ExecContext(
			ctx,
			`INSERT INTO assessments (id, created_at, data) VALUES ($1, $2, $3::jsonb)`,
			createdRecord.ID,
			now,
			string(raw),
		)
		if err == nil {
			return createdRecord, nil
		}
		if isUniqueViolation(err) {
			now = now.Add(time.Second)
			continue
		}
		return record{}, err
	}

	return record{}, errors.New("unable to create unique timestamp id")
}

func (s *postgresStore) Get(id string) (record, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, created_at, data FROM assessments WHERE id = $1`,
		id,
	)

	savedRecord, err := scanRecord(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return record{}, false, nil
		}
		return record{}, false, err
	}

	return savedRecord, true, nil
}

func (s *postgresStore) SearchByName(name string) ([]record, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, created_at, data
		 FROM assessments
		 WHERE lower(data #>> '{respondent,name}') LIKE '%' || $1 || '%'
		 ORDER BY id DESC`,
		name,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	matches := []record{}
	for rows.Next() {
		savedRecord, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		matches = append(matches, savedRecord)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return matches, nil
}

func (s *postgresStore) Close() error {
	return s.db.Close()
}

type recordScanner interface {
	Scan(dest ...any) error
}

func scanRecord(scanner recordScanner) (record, error) {
	var id string
	var createdAt time.Time
	var raw []byte

	if err := scanner.Scan(&id, &createdAt, &raw); err != nil {
		return record{}, err
	}

	return record{
		ID:        id,
		CreatedAt: createdAt.Format(time.RFC3339Nano),
		Data:      json.RawMessage(raw),
	}, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
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
