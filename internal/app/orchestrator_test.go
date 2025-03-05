package app

import (
	"bytes"
	"net/http"
	"net/http/httptest"

	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestCalculateAndCheckResult(t *testing.T) {
	go func() {
		Runorchestrator()
	}()
	go func() {
		Runagent()
	}()
	time.Sleep(7 * time.Second)
	tests := []struct {
		name         string
		input        string
		expectCode   int
		expectResult float64
		expectStatus string
	}{
		{
			name:         "Сложение",
			input:        `{"expression": "2 + 2"}`,
			expectCode:   http.StatusCreated,
			expectResult: 4,
			expectStatus: "completed",
		},
		{
			name:         "вычитание и умножение",
			input:        `{"expression": "2 * 2 - 2 * 2"}`,
			expectCode:   http.StatusCreated,
			expectResult: 0,
			expectStatus: "completed",
		},
		{
			name:         "деление",
			input:        `{"expression": "2/2"}`,
			expectCode:   http.StatusCreated,
			expectResult: 1,
			expectStatus: "completed",
		},
		{
			name:         "деление на 0",
			input:        `{"expression": "1/0"}`,
			expectCode:   http.StatusCreated,
			expectResult: 0,
			expectStatus: "error",
		},
	}

	for _, tc := range tests {
		
		resp, err := http.Post("http://localhost:8082/api/v1/calculate", "application/json", bytes.NewBuffer([]byte(tc.input)))
		if err != nil {
			t.Fatalf("Ошибка при отправке POST-запроса: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != tc.expectCode {
			t.Errorf("%s: ожидался статус %d, получено: %d", tc.name, tc.expectCode, resp.StatusCode)
			return
		}

		var postResponse Expression
		if err := json.NewDecoder(resp.Body).Decode(&postResponse); err != nil {
			t.Fatalf("Ошибка декодирования ответа от POST-запроса: %v", err)
		}

		time.Sleep(5 * time.Second)

		getResp, err := http.Get(fmt.Sprintf("http://localhost:8082/api/v1/expressions/%s", postResponse.ID))
		if err != nil {
			t.Fatalf("Ошибка при отправке GET-запроса: %v", err)
		}
		defer getResp.Body.Close()

		if getResp.StatusCode != http.StatusOK {
			t.Errorf("%s: GET-запрос вернул статус %d", tc.name, getResp.StatusCode)
			return
		}

		var getResponse Expression
		if err := json.NewDecoder(getResp.Body).Decode(&getResponse); err != nil {
			t.Fatalf("Ошибка декодирования ответа от GET-запроса: %v", err)
		}
		if getResponse.Status != tc.expectStatus {
			t.Errorf("%s: ожидался результат %s, получено: %s", tc.name, tc.expectStatus, getResponse.Status)
			
		}
		if getResponse.Status == "error"{
			continue
		}

		if *getResponse.Result != tc.expectResult {
			t.Errorf("%s: ожидался результат %f, получено: %f", tc.name, tc.expectResult, *getResponse.Result)
		}
		
		
		
	}
	
}

func TestHandleTask_Get(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/internal/task", nil)
	w := httptest.NewRecorder()

	handleTask(w, req)

	if w.Code == http.StatusNotFound {
		t.Log("Ожидаемое поведение: нет задач")
	} else if w.Code != http.StatusOK {
		t.Errorf("неожиданный статус: %d", w.Code)
	}
}

func TestHandleExpressions(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/expressions", nil)
	w := httptest.NewRecorder()

	handleExpressions(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("ожидался статус 200, получено: %d", w.Code)
	}
}

func TestExpressionByIDHandler_NotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/expressions/999", nil)
	w := httptest.NewRecorder()

	expressionByIDHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("ожидался статус 404, получено: %d", w.Code)
	}
}
