package app

import (
	"os"
	"testing"
	"time"

	pb "github.com/fufelshmerc-corparate/go_calc/api/calc"
	"github.com/stretchr/testify/assert"
)

func TestPerformOperation(t *testing.T) {
	tests := []struct {
		name     string
		task     *pb.Task
		expected float64
		wantErr  bool
	}{
		{"Addition", &pb.Task{Arg1: 2, Arg2: 3, Operation: "+", OperationTime: 0}, 5, false},
		{"Subtraction", &pb.Task{Arg1: 5, Arg2: 3, Operation: "-", OperationTime: 0}, 2, false},
		{"Multiplication", &pb.Task{Arg1: 2, Arg2: 3, Operation: "*", OperationTime: 0}, 6, false},
		{"Division", &pb.Task{Arg1: 6, Arg2: 3, Operation: "/", OperationTime: 0}, 2, false},
		{"DivisionByZero", &pb.Task{Id: "t1", Arg1: 6, Arg2: 0, Operation: "/", OperationTime: 0}, 0, true},
		{"UnknownOperation", &pb.Task{Id: "t2", Arg1: 1, Arg2: 1, Operation: "%", OperationTime: 0}, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := performOperation(tt.task)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetEnvDuration(t *testing.T) {
	os.Setenv("TEST_TIMEOUT_MS", "250")
	defer os.Unsetenv("TEST_TIMEOUT_MS")

	duration := getEnvDuration("TEST_TIMEOUT_MS", 100*time.Millisecond)
	assert.Equal(t, 250*time.Millisecond, duration)

	os.Setenv("TEST_TIMEOUT_MS", "invalid")
	duration = getEnvDuration("TEST_TIMEOUT_MS", 150*time.Millisecond)
	assert.Equal(t, 150*time.Millisecond, duration)

	os.Unsetenv("TEST_TIMEOUT_MS")
	duration = getEnvDuration("TEST_TIMEOUT_MS", 200*time.Millisecond)
	assert.Equal(t, 200*time.Millisecond, duration)
}
