package app

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
	"context"
)

func TestGetEnvDuration(t *testing.T) {
	os.Setenv("TEST_ENV", "500")
	defer os.Unsetenv("TEST_ENV")

	duration := getEnvDuration("TEST_ENV", 100)
	if duration != 500*time.Millisecond {
		t.Errorf("Ожидалось 500ms, получено %v", duration)
	}

	duration = getEnvDuration("MISSING_ENV", 200)
	if duration != 200*time.Millisecond {
		t.Errorf("Ожидалось 200ms, получено %v", duration)
	}
}
func TestGetServerURL(t *testing.T) {
	os.Setenv("PORT", "8080")
	defer os.Unsetenv("PORT")

	url := getServerURL()
	if url != "http://localhost:8080" {
		t.Errorf("Ожидалось http://localhost:8080, получено %s", url)
	}

	os.Unsetenv("PORT")
	url = getServerURL()
	if url != "http://localhost:8082" {
		t.Errorf("Ожидалось http://localhost:8082, получено %s", url)
	}
}
func TestPerformOperation(t *testing.T) {
	tests := []struct {
		name     string
		task     Task
		expected float64
		err      string
	}{
		{"Сложение", Task{Operation: "+", Arg1: 2, Arg2: 3}, 5, ""},
		{"Вычитание", Task{Operation: "-", Arg1: 5, Arg2: 3}, 2, ""},
		{"Умножение", Task{Operation: "*", Arg1: 2, Arg2: 3}, 6, ""},
		{"Деление", Task{Operation: "/", Arg1: 6, Arg2: 3}, 2, ""},
		{"Деление на ноль", Task{Operation: "/", Arg1: 6, Arg2: 0}, 0, "деление на ноль"},
		{"Неизвестная операция", Task{Operation: "%", Arg1: 6, Arg2: 3}, 0, "неизвестная операция: %"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := performOperation(&tt.task)
			if err != nil && err.Error() != tt.err {
				t.Errorf("Ожидалась ошибка '%s', получено '%v'", tt.err, err)
			}
			if result != tt.expected {
				t.Errorf("Ожидалось %f, получено %f", tt.expected, result)
			}
		})
	}
}
func startTestServer() func() {
	server := &http.Server{
		Addr: "localhost:8082",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
				return
			}
			var res Result
			if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
				http.Error(w, "ошибка декодирования", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		}),
	}

	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			panic(err) 
		}
	}()

	time.Sleep(1000 * time.Millisecond)

	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx) 
	}
}

func TestPostResult(t *testing.T) {
	stopServer := startTestServer()
	defer stopServer()
	result := &Result{ID: "123", Result: 42}

	err := postResult(result)
	if err != nil {
		t.Errorf("postResult вернул ошибку: %v", err)
	}
}
