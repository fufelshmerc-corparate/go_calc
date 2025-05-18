package app

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/IvanSolomatin/calc_v2/api/calc"
)

func setupTestOrchestrator(t *testing.T) (*Orchestrator, *sql.DB, sqlmock.Sqlmock) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("Failed to create sqlmock: %v", err)
	}

	jwtSecret := []byte("test-secret")
	orch := NewOrchestrator(db, jwtSecret)

	return orch, db, mock
}

func TestHandleRegister(t *testing.T) {
	orch, db, mock := setupTestOrchestrator(t)
	defer db.Close()

	tests := []struct {
		name           string
		body           string
		mockSetup      func()
		expectedStatus int
		expectedBody   string
	}{
		{
			name: "Valid registration",
			body: `{"login":"testuser","password":"testpass"}`,
			mockSetup: func() {
				mock.ExpectExec("INSERT INTO users").
					WithArgs("testuser", sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(1, 1))
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "OK",
		},
		{
			name:           "Invalid JSON",
			body:           `{"login":"testuser","password":}`,
			mockSetup:      func() {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Invalid request body",
		},
		{
			name:           "Missing login",
			body:           `{"password":"testpass"}`,
			mockSetup:      func() {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Login and password are required",
		},
		{
			name: "User already exists",
			body: `{"login":"testuser","password":"testpass"}`,
			mockSetup: func() {
				mock.ExpectExec("INSERT INTO users").
					WithArgs("testuser", sqlmock.AnyArg()).
					WillReturnError(fmt.Errorf("UNIQUE constraint failed"))
			},
			expectedStatus: http.StatusConflict,
			expectedBody:   "User already exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mockSetup()

			req := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(tt.body))
			w := httptest.NewRecorder()

			orch.handleRegister(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if !strings.Contains(w.Body.String(), tt.expectedBody) {
				t.Errorf("Expected body to contain %q, got %q", tt.expectedBody, w.Body.String())
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("Unfulfilled expectations: %v", err)
			}
		})
	}
}

func TestHandleLogin(t *testing.T) {
	orch, db, mock := setupTestOrchestrator(t)
	defer db.Close()

	hashedPassword, _ := bcrypt.GenerateFromPassword([]byte("testpass"), bcrypt.DefaultCost)

	tests := []struct {
		name           string
		body           string
		mockSetup      func()
		expectedStatus int
		expectedBody   string
	}{
		{
			name: "Valid login",
			body: `{"login":"testuser","password":"testpass"}`,
			mockSetup: func() {
				mock.ExpectQuery("SELECT id, password FROM users").
					WithArgs("testuser").
					WillReturnRows(sqlmock.NewRows([]string{"id", "password"}).
						AddRow(1, string(hashedPassword)))
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `"token":`,
		},
		{
			name:           "Invalid JSON",
			body:           `{"login":"testuser","password":}`,
			mockSetup:      func() {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Invalid request body",
		},
		{
			name: "Invalid credentials",
			body: `{"login":"testuser","password":"wrongpass"}`,
			mockSetup: func() {
				mock.ExpectQuery("SELECT id, password FROM users").
					WithArgs("testuser").
					WillReturnRows(sqlmock.NewRows([]string{"id", "password"}).
						AddRow(1, string(hashedPassword)))
			},
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "Invalid login or password",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mockSetup()

			req := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(tt.body))
			w := httptest.NewRecorder()

			orch.handleLogin(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if !strings.Contains(w.Body.String(), tt.expectedBody) {
				t.Errorf("Expected body to contain %q, got %q", tt.expectedBody, w.Body.String())
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("Unfulfilled expectations: %v", err)
			}
		})
	}
}

func TestGetTask(t *testing.T) {
	orch, _, _ := setupTestOrchestrator(t)

	task := &pb.Task{
		Id:        "task1",
		Arg1:      10,
		Arg2:      5,
		Operation: "+",
	}
	orch.addTask(task)

	tests := []struct {
		name        string
		setup       func()
		expectedErr error
		expectedID  string
	}{
		{
			name:       "Valid task",
			setup:      func() {},
			expectedID: "task1",
		},
		{
			name: "No tasks",
			setup: func() {
				orch.mu.Lock()
				orch.tasks = nil
				orch.mu.Unlock()
			},
			expectedErr: status.Error(codes.NotFound, "no tasks available"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()

			task, err := orch.GetTask(context.Background(), &pb.TaskRequest{})

			if tt.expectedErr != nil {
				if err == nil || err.Error() != tt.expectedErr.Error() {
					t.Errorf("Expected error %v, got %v", tt.expectedErr, err)
				}
				return
			}

			if task.Id != tt.expectedID {
				t.Errorf("Expected task ID %q, got %q", tt.expectedID, task.Id)
			}
		})
	}
}

func TestSubmitResult(t *testing.T) {
	orch, db, mock := setupTestOrchestrator(t)
	defer db.Close()

	orch.processing["task1"] = true

	tests := []struct {
		name            string
		result          *pb.Result
		mockSetup       func()
		expectedErr     error
		expectedSuccess bool
	}{
		{
			name: "Valid result",
			result: &pb.Result{
				Id:     "task1",
				Result: 15.0,
			},
			mockSetup: func() {
				mock.ExpectExec("UPDATE expressions").
					WithArgs(15.0, "task1").
					WillReturnResult(sqlmock.NewResult(1, 1))
			},
			expectedSuccess: true,
		},
		{
			name: "Task not found",
			result: &pb.Result{
				Id:     "task2",
				Result: 15.0,
			},
			mockSetup:       func() {},
			expectedErr:     status.Error(codes.NotFound, "task not found"),
			expectedSuccess: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mockSetup()

			resp, err := orch.SubmitResult(context.Background(), tt.result)

			if tt.expectedErr != nil {
				if err == nil || err.Error() != tt.expectedErr.Error() {
					t.Errorf("Expected error %v, got %v", tt.expectedErr, err)
				}
				return
			}

			if resp.Success != tt.expectedSuccess {
				t.Errorf("Expected success %v, got %v", tt.expectedSuccess, resp.Success)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("Unfulfilled expectations: %v", err)
			}
		})
	}
}

func TestHandleCalculate(t *testing.T) {
	orch, db, mock := setupTestOrchestrator(t)
	defer db.Close()

	tests := []struct {
		name           string
		body           string
		userID         int
		mockSetup      func()
		expectedStatus int
		expectedBody   string
	}{
		{
			name:   "Valid expression",
			body:   `{"expression":"2+2"}`,
			userID: 1,
			mockSetup: func() {
				mock.ExpectExec("INSERT INTO expressions").
					WithArgs(sqlmock.AnyArg(), 1, "2+2", "processing").
					WillReturnResult(sqlmock.NewResult(1, 1))
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `"id":`,
		},
		{
			name:           "Invalid JSON",
			body:           `{"expression":}`,
			userID:         1,
			mockSetup:      func() {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Invalid request body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mockSetup()

			req := httptest.NewRequest(http.MethodPost, "/api/v1/calculate", strings.NewReader(tt.body))
			ctx := context.WithValue(req.Context(), "user_id", tt.userID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			orch.handleCalculate(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if !strings.Contains(w.Body.String(), tt.expectedBody) {
				t.Errorf("Expected body to contain %q, got %q", tt.expectedBody, w.Body.String())
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("Unfulfilled expectations: %v", err)
			}
		})
	}
}
