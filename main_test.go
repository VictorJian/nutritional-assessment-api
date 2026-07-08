package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestCreateGetAndSearchAssessment(t *testing.T) {
	application := &app{
		dataFile: filepath.Join(t.TempDir(), "assessments.json"),
		records:  map[string]record{},
	}

	handler := application.routes()
	body := `{"respondent":{"name":"王小明","age":30,"date":"2026-07-06"},"recommendations":[]}`
	createRequest := httptest.NewRequest(http.MethodPost, "/api/assessments", strings.NewReader(body))
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()

	handler.ServeHTTP(createResponse, createRequest)

	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", createResponse.Code, http.StatusCreated)
	}

	var created record
	if err := json.Unmarshal(createResponse.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if matched, err := regexp.MatchString(`^\d{14}$`, created.ID); err != nil || !matched {
		t.Fatalf("created id = %q, want YYYYMMDDHHMMSS timestamp", created.ID)
	}

	getRequest := httptest.NewRequest(http.MethodGet, "/api/assessments/"+created.ID, nil)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, getRequest)

	if getResponse.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d", getResponse.Code, http.StatusOK)
	}

	searchRequest := httptest.NewRequest(http.MethodGet, "/api/assessments?name="+url.QueryEscape("王小明"), nil)
	searchResponseRecorder := httptest.NewRecorder()
	handler.ServeHTTP(searchResponseRecorder, searchRequest)

	if searchResponseRecorder.Code != http.StatusOK {
		t.Fatalf("search status = %d, want %d", searchResponseRecorder.Code, http.StatusOK)
	}

	var result searchResponse
	if err := json.Unmarshal(searchResponseRecorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	if result.Count != 1 || len(result.Records) != 1 || result.Records[0].ID != created.ID {
		t.Fatalf("search result = %+v, want created record", result)
	}
}

func TestTimestampIDUsesYearMonthDayHourMinuteSecond(t *testing.T) {
	loc := time.FixedZone("Asia/Taipei", 8*60*60)
	timestamp := time.Date(2026, 7, 7, 9, 8, 6, 123456789, loc)

	if got, want := timestampID(timestamp), "20260707090806"; got != want {
		t.Fatalf("timestampID() = %q, want %q", got, want)
	}
}

func TestPing(t *testing.T) {
	application := &app{
		dataFile: filepath.Join(t.TempDir(), "assessments.json"),
		records:  map[string]record{},
	}

	request := httptest.NewRequest(http.MethodGet, "/ping", nil)
	response := httptest.NewRecorder()

	application.routes().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("ping status = %d, want %d", response.Code, http.StatusOK)
	}

	var result map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode ping response: %v", err)
	}
	if got, want := result["message"], "pong"; got != want {
		t.Fatalf("ping message = %q, want %q", got, want)
	}
}

func TestCORSPreflight(t *testing.T) {
	application := &app{
		dataFile: filepath.Join(t.TempDir(), "assessments.json"),
		records:  map[string]record{},
	}

	request := httptest.NewRequest(http.MethodOptions, "/api/assessments", nil)
	request.Header.Set("Origin", "http://localhost:3000")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "Content-Type, X-Requested-With")
	response := httptest.NewRecorder()

	application.routes().ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if got, want := response.Header().Get("Access-Control-Allow-Origin"), "*"; got != want {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, want)
	}
	if got, want := response.Header().Get("Access-Control-Allow-Methods"), "GET, POST, OPTIONS"; got != want {
		t.Fatalf("Access-Control-Allow-Methods = %q, want %q", got, want)
	}
	if got, want := response.Header().Get("Access-Control-Allow-Headers"), "Content-Type, X-Requested-With"; got != want {
		t.Fatalf("Access-Control-Allow-Headers = %q, want %q", got, want)
	}
}
