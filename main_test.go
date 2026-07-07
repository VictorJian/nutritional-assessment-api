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
